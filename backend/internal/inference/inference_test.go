package inference

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/traego/traego/internal/ollama"
)

type fakeOllama struct {
	mu           sync.Mutex
	pulled       map[string]bool
	healthy      bool
	genResp      string
	genErr       error
	pullErr      error
	lastMsgCount int
}

func newFake() *fakeOllama { return &fakeOllama{pulled: map[string]bool{}, healthy: true, genResp: "hi"} }

func (f *fakeOllama) Healthy(context.Context) bool { return f.healthy }
func (f *fakeOllama) Tags(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.pulled))
	for k := range f.pulled {
		out = append(out, k)
	}
	return out, nil
}
func (f *fakeOllama) Pull(_ context.Context, model string, onProgress func(ollama.PullProgress)) error {
	if f.pullErr != nil {
		return f.pullErr
	}
	if onProgress != nil {
		onProgress(ollama.PullProgress{Status: "downloading", Completed: 50, Total: 100})
		onProgress(ollama.PullProgress{Status: "success", Completed: 100, Total: 100})
	}
	f.mu.Lock()
	f.pulled[model] = true
	f.mu.Unlock()
	return nil
}
func (f *fakeOllama) Chat(_ context.Context, model string, msgs []ollama.Message) (ollama.Completion, error) {
	if f.genErr != nil {
		return ollama.Completion{}, f.genErr
	}
	f.lastMsgCount = len(msgs)
	return ollama.Completion{Response: f.genResp, EvalCount: 10, EvalDuration: 250 * time.Millisecond}, nil
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func status(m *Manager, id string) ModelStatus {
	for _, s := range m.Models(context.Background(), 999) {
		if s.ID == id {
			return s
		}
	}
	return ModelStatus{}
}

func TestModelsFitByMemory(t *testing.T) {
	m := NewManager(newFake())
	got := m.Models(context.Background(), 4) // 4 GB available
	for _, s := range got {
		want := s.MinMemGB <= 4
		if s.Fits != want {
			t.Fatalf("%s fits=%v, want %v (min %v, avail 4)", s.ID, s.Fits, want, s.MinMemGB)
		}
	}
}

func TestDeployEnableChat(t *testing.T) {
	f := newFake()
	m := NewManager(f)
	const id = "llama3.2:1b"

	if err := m.Deploy(id); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !waitFor(time.Second, func() bool { s := status(m, id); return s.Deployed && !s.Deploying }) {
		t.Fatalf("model never deployed: %+v", status(m, id))
	}
	if s := status(m, id); s.Progress != 100 || s.Status != "deployed" {
		t.Fatalf("deploy state wrong: %+v", s)
	}

	if err := m.Enable(context.Background(), id); err != nil {
		t.Fatalf("enable: %v", err)
	}
	if m.Enabled() != id || !status(m, id).Enabled {
		t.Fatalf("not enabled")
	}

	// pass a full conversation; the model should receive all turns
	convo := []Message{
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello!"},
		{Role: "user", Content: "what did I just say?"},
	}
	reply, err := m.Chat(context.Background(), "", convo)
	if err != nil || reply != "hi" {
		t.Fatalf("chat: %q %v", reply, err)
	}
	if f.lastMsgCount != 3 {
		t.Fatalf("model received %d messages, want full history of 3", f.lastMsgCount)
	}
	// real token telemetry accrues
	if m.TokensGenerated() != 10 {
		t.Fatalf("tokens generated = %d, want 10", m.TokensGenerated())
	}
	if m.Inflight() != 0 {
		t.Fatalf("inflight should be 0 after chat, got %d", m.Inflight())
	}
}

func TestDeployUnknown(t *testing.T) {
	m := NewManager(newFake())
	if err := m.Deploy("totally:fake"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("want ErrUnknownModel, got %v", err)
	}
}

func TestEnableRejections(t *testing.T) {
	m := NewManager(newFake())
	if err := m.Enable(context.Background(), "totally:fake"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown: want ErrUnknownModel, got %v", err)
	}
	// known but not pulled
	if err := m.Enable(context.Background(), "qwen2.5:7b"); !errors.Is(err, ErrNotDeployed) {
		t.Fatalf("not deployed: want ErrNotDeployed, got %v", err)
	}
}

func TestChatNoModel(t *testing.T) {
	m := NewManager(newFake())
	if _, err := m.Chat(context.Background(), "", []Message{{Role: "user", Content: "x"}}); !errors.Is(err, ErrNoModelEnabled) {
		t.Fatalf("want ErrNoModelEnabled, got %v", err)
	}
}

func TestChatGenerateError(t *testing.T) {
	f := newFake()
	f.pulled["llama3.2:1b"] = true
	f.genErr = errors.New("boom")
	m := NewManager(f)
	_ = m.Enable(context.Background(), "llama3.2:1b")
	if _, err := m.Chat(context.Background(), "", []Message{{Role: "user", Content: "x"}}); err == nil {
		t.Fatal("expected generate error to surface")
	}
}

func TestMultiEnableAndSelection(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	f.pulled["llama3.2:1b"] = true
	f.pulled["gemma2:2b"] = true
	m := NewManager(f)

	// enable BOTH — multiple models can be offered to users at once
	if err := m.Enable(ctx, "llama3.2:1b"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, "gemma2:2b"); err != nil {
		t.Fatal(err)
	}
	// the first enabled is the default for chats that name no model
	if m.Enabled() != "llama3.2:1b" {
		t.Fatalf("default = %q, want first-enabled llama3.2:1b", m.Enabled())
	}
	list := m.EnabledList()
	if len(list) != 2 || list[0].ID != "llama3.2:1b" || list[1].ID != "gemma2:2b" {
		t.Fatalf("enabled list = %+v, want both in catalog order", list)
	}

	// chat works with the default ("") and with either enabled model by name
	for _, id := range []string{"", "gemma2:2b", "llama3.2:1b"} {
		if reply, err := m.Chat(ctx, id, []Message{{Role: "user", Content: "hi"}}); err != nil || reply != "hi" {
			t.Fatalf("chat model=%q: %q %v", id, reply, err)
		}
	}

	// deployed but NOT enabled -> not selectable for users
	f.pulled["llama3.2:3b"] = true
	if _, err := m.Chat(ctx, "llama3.2:3b", []Message{{Role: "user", Content: "hi"}}); !errors.Is(err, ErrNotDeployed) {
		t.Fatalf("not-enabled select: want ErrNotDeployed, got %v", err)
	}
	if _, err := m.Chat(ctx, "bogus:1t", []Message{{Role: "user", Content: "hi"}}); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("unknown select: want ErrUnknownModel, got %v", err)
	}

	// disabling the default promotes another enabled model to default
	if err := m.Disable("llama3.2:1b"); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() != "gemma2:2b" || len(m.EnabledList()) != 1 {
		t.Fatalf("after disabling default: default=%q list=%d", m.Enabled(), len(m.EnabledList()))
	}

	// disabling the last leaves nothing enabled
	if err := m.Disable("gemma2:2b"); err != nil {
		t.Fatal(err)
	}
	if m.Enabled() != "" {
		t.Fatalf("no models enabled but default = %q", m.Enabled())
	}
	if _, err := m.Chat(ctx, "", []Message{{Role: "user", Content: "hi"}}); !errors.Is(err, ErrNoModelEnabled) {
		t.Fatalf("want ErrNoModelEnabled, got %v", err)
	}
	if err := m.Disable("bogus:1t"); !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("disable unknown: want ErrUnknownModel, got %v", err)
	}
}

func TestDeployErrorRecorded(t *testing.T) {
	f := newFake()
	f.pullErr = errors.New("disk full")
	m := NewManager(f)
	_ = m.Deploy("llama3.2:1b")
	if !waitFor(time.Second, func() bool { return status(m, "llama3.2:1b").Status == "error" }) {
		t.Fatalf("deploy error not recorded: %+v", status(m, "llama3.2:1b"))
	}
}

func TestHealthy(t *testing.T) {
	if !NewManager(newFake()).Healthy(context.Background()) {
		t.Fatal("want healthy")
	}
	f := newFake()
	f.healthy = false
	if NewManager(f).Healthy(context.Background()) {
		t.Fatal("want unhealthy")
	}
}

func TestInstallUninstall(t *testing.T) {
	m := NewManager(newFake())
	if m.Installed() {
		t.Fatal("should start uninstalled")
	}
	if err := m.Install(context.Background(), 16); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !m.Installed() || m.VRAMReservedGB() != 16 {
		t.Fatalf("installed=%v vram=%v", m.Installed(), m.VRAMReservedGB())
	}
	// uninstall clears the reservation and the enabled set
	_ = m.Deploy("llama3.2:1b")
	_ = m.Enable(context.Background(), "llama3.2:1b")
	m.Uninstall()
	if m.Installed() || m.VRAMReservedGB() != 0 {
		t.Fatalf("after uninstall: installed=%v vram=%v", m.Installed(), m.VRAMReservedGB())
	}
	if m.Enabled() != "" {
		t.Fatalf("uninstall should clear enabled set, got %q", m.Enabled())
	}
}

func TestInstallBackendDown(t *testing.T) {
	f := newFake()
	f.healthy = false
	m := NewManager(f)
	if err := m.Install(context.Background(), 8); !errors.Is(err, ErrBackendDown) {
		t.Fatalf("install with backend down: want ErrBackendDown, got %v", err)
	}
	if m.Installed() {
		t.Fatal("should not be installed after a failed install")
	}
}
