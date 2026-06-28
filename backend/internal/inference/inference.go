// Package inference manages the controller's local AI: a catalog of models
// tagged by memory footprint, one-click deploy (pull) with progress, a set of
// models enabled for end users, and chat routed to one of them. It drives an
// Ollama backend.
package inference

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/traego/traego/internal/ollama"
)

// Model is a catalog entry. ID is the Ollama tag used to pull/run it.
type Model struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Params   string  `json:"params"`
	SizeGB   float64 `json:"size_gb"`     // download size
	MinMemGB float64 `json:"min_mem_gb"`  // memory needed to run comfortably
	Blurb    string  `json:"blurb"`
}

// Catalog is the curated set of models, smallest first. Memory needs are
// approximate (q4 weights + KV cache headroom).
var Catalog = []Model{
	{ID: "qwen2.5:0.5b", Name: "Qwen2.5 0.5B", Params: "0.5B", SizeGB: 0.4, MinMemGB: 1, Blurb: "Tiny and instant. Great for quick Q&A on modest machines."},
	{ID: "llama3.2:1b", Name: "Llama 3.2 1B", Params: "1B", SizeGB: 1.3, MinMemGB: 2, Blurb: "Fast, capable little assistant."},
	{ID: "gemma2:2b", Name: "Gemma 2 2B", Params: "2B", SizeGB: 1.6, MinMemGB: 3, Blurb: "Google's small model, strong for its size."},
	{ID: "llama3.2:3b", Name: "Llama 3.2 3B", Params: "3B", SizeGB: 2.0, MinMemGB: 4, Blurb: "A well-rounded general assistant."},
	{ID: "qwen2.5:7b", Name: "Qwen2.5 7B", Params: "7B", SizeGB: 4.7, MinMemGB: 8, Blurb: "Noticeably smarter; good default on 16GB+."},
	{ID: "llama3.1:8b", Name: "Llama 3.1 8B", Params: "8B", SizeGB: 4.9, MinMemGB: 10, Blurb: "Strong general model."},
	{ID: "qwen2.5:14b", Name: "Qwen2.5 14B", Params: "14B", SizeGB: 9.0, MinMemGB: 16, Blurb: "High quality; needs a roomy machine."},
}

// Errors.
var (
	ErrUnknownModel   = errors.New("inference: unknown model")
	ErrNotDeployed    = errors.New("inference: model is not deployed")
	ErrNoModelEnabled = errors.New("inference: no model enabled")
)

// Message is one conversation turn (alias of the Ollama type).
type Message = ollama.Message

// Puller is the subset of the Ollama client the manager needs (so tests can fake it).
type Puller interface {
	Healthy(ctx context.Context) bool
	Tags(ctx context.Context) ([]string, error)
	Pull(ctx context.Context, model string, onProgress func(ollama.PullProgress)) error
	Chat(ctx context.Context, model string, messages []ollama.Message) (ollama.Completion, error)
}

// ModelStatus is a catalog entry enriched with live state.
type ModelStatus struct {
	Model
	Fits      bool    `json:"fits"`
	Deployed  bool    `json:"deployed"`
	Enabled   bool    `json:"enabled"` // offered to end users
	Default   bool    `json:"default"` // the default model for chats that name none
	Deploying bool    `json:"deploying"`
	Progress  float64 `json:"progress"`
	Status    string  `json:"status"`
}

type deployState struct {
	active bool
	pct    float64
	status string
	err    string
}

// Manager owns the model lifecycle for one controller.
type Manager struct {
	ol           Puller
	mu           sync.Mutex
	deploy       map[string]*deployState
	enabled      map[string]bool // models offered to end users
	defaultModel string          // used when a chat request doesn't name a model

	// real inference telemetry (atomic; read by the activity sampler)
	totalTokens atomic.Int64
	inflight    atomic.Int64
}

// TokensGenerated is the cumulative count of tokens produced by Chat. The
// activity sampler differentiates this over time for a real tokens/sec.
func (m *Manager) TokensGenerated() int64 { return m.totalTokens.Load() }

// Inflight is the number of generations currently running.
func (m *Manager) Inflight() int { return int(m.inflight.Load()) }

// NewManager builds a manager over an Ollama backend.
func NewManager(ol Puller) *Manager {
	return &Manager{ol: ol, deploy: map[string]*deployState{}, enabled: map[string]bool{}}
}

// Healthy reports whether the inference backend is reachable.
func (m *Manager) Healthy(ctx context.Context) bool { return m.ol.Healthy(ctx) }

// Enabled returns the default model id used when a chat request names none
// (empty if no model is enabled).
func (m *Manager) Enabled() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultModel
}

// EnabledList returns the models offered to end users, in catalog order.
func (m *Manager) EnabledList() []ModelRef {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []ModelRef{}
	for _, mod := range Catalog {
		if m.enabled[mod.ID] {
			out = append(out, ModelRef{ID: mod.ID, Name: mod.Name})
		}
	}
	return out
}

func (m *Manager) isEnabled(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled[id]
}

// Models returns the catalog enriched with fit (against availMemGB), deployment,
// and enable/progress state.
func (m *Manager) Models(ctx context.Context, availMemGB float64) []ModelStatus {
	deployed := map[string]bool{}
	if tags, err := m.ol.Tags(ctx); err == nil {
		for _, t := range tags {
			deployed[t] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ModelStatus, 0, len(Catalog))
	for _, mod := range Catalog {
		st := ModelStatus{
			Model:    mod,
			Fits:     mod.MinMemGB <= availMemGB,
			Deployed: deployed[mod.ID],
			Enabled:  m.enabled[mod.ID],
			Default:  mod.ID == m.defaultModel,
		}
		if d := m.deploy[mod.ID]; d != nil {
			st.Deploying = d.active
			st.Progress = d.pct
			st.Status = d.status
		}
		out = append(out, st)
	}
	return out
}

// Deploy starts an async pull of the model. Idempotent while a pull is running.
func (m *Manager) Deploy(id string) error {
	if !known(id) {
		return ErrUnknownModel
	}
	m.mu.Lock()
	if d := m.deploy[id]; d != nil && d.active {
		m.mu.Unlock()
		return nil
	}
	ds := &deployState{active: true, status: "starting"}
	m.deploy[id] = ds
	m.mu.Unlock()

	go func() {
		err := m.ol.Pull(context.Background(), id, func(p ollama.PullProgress) {
			m.mu.Lock()
			ds.pct = p.Percent()
			ds.status = p.Status
			m.mu.Unlock()
		})
		m.mu.Lock()
		ds.active = false
		if err != nil {
			ds.err, ds.status = err.Error(), "error"
		} else {
			ds.status, ds.pct = "deployed", 100
		}
		m.mu.Unlock()
	}()
	return nil
}

// Enable adds a deployed model to the set offered to end users. Multiple models
// can be enabled at once; the first enabled becomes the default for chats that
// don't name a model.
func (m *Manager) Enable(ctx context.Context, id string) error {
	if !known(id) {
		return ErrUnknownModel
	}
	if !m.isDeployed(ctx, id) {
		return ErrNotDeployed
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled[id] = true
	if m.defaultModel == "" {
		m.defaultModel = id
	}
	return nil
}

// Disable removes a model from the set offered to end users. If it was the
// default, another enabled model (if any) takes over as default.
func (m *Manager) Disable(id string) error {
	if !known(id) {
		return ErrUnknownModel
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.enabled, id)
	if m.defaultModel == id {
		m.defaultModel = ""
		for _, mod := range Catalog {
			if m.enabled[mod.ID] {
				m.defaultModel = mod.ID
				break
			}
		}
	}
	return nil
}

// ModelRef is a lightweight model reference (id + display name).
type ModelRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (m *Manager) isDeployed(ctx context.Context, id string) bool {
	tags, err := m.ol.Tags(ctx)
	if err != nil {
		return false
	}
	for _, t := range tags {
		if t == id {
			return true
		}
	}
	return false
}

// Chat routes a conversation (full message history) to a model, recording real
// throughput. If model is "", the enabled default is used; a named model must be
// in the catalog and deployed (lets the chat console pick a model).
func (m *Manager) Chat(ctx context.Context, model string, messages []Message) (string, error) {
	if model == "" {
		model = m.Enabled()
		if model == "" {
			return "", ErrNoModelEnabled
		}
	} else {
		if !known(model) {
			return "", ErrUnknownModel
		}
		if !m.isEnabled(model) {
			return "", ErrNotDeployed // not in the set offered to users
		}
	}
	m.inflight.Add(1)
	defer m.inflight.Add(-1)
	c, err := m.ol.Chat(ctx, model, messages)
	if err != nil {
		return "", err
	}
	m.totalTokens.Add(int64(c.EvalCount))
	return c.Response, nil
}

func known(id string) bool {
	for _, m := range Catalog {
		if m.ID == id {
			return true
		}
	}
	return false
}
