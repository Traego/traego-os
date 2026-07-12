// Package store holds the controller's node inventory and the canonical
// node lifecycle state. It is deliberately backed by an interface so the
// in-memory implementation used for v0 and tests can be swapped for SQLite
// without touching the API layer.
package store

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/traego/traego/internal/metrics"
)

// Class distinguishes always-on machines from opportunistic burst capacity.
type Class string

const (
	ClassPersistent Class = "persistent" // always-on servers
	ClassEphemeral  Class = "ephemeral"  // laptops/workstations that come and go
)

// Valid reports whether c is a known class.
func (c Class) Valid() bool { return c == ClassPersistent || c == ClassEphemeral }

// State is the node's position in the adoption + presence lifecycle.
//
//	pending  -> discovered on the LAN, awaiting a verified adopt
//	adopted  -> pairing verified, credential issued, not yet heartbeating
//	online   -> heartbeating within the timeout window
//	offline  -> adopted but missed its heartbeats (or left gracefully)
type State string

const (
	StatePending State = "pending"
	StateAdopted State = "adopted"
	StateOnline  State = "online"
	StateOffline State = "offline"
)

// Role is the workload class assigned at adoption.
type Role string

const (
	RoleController Role = "controller"
	RoleInference  Role = "inference"
	RoleApp        Role = "app"
	RoleStorage    Role = "storage"
)

// ValidRole reports whether r is an assignable role.
func ValidRole(r Role) bool {
	switch r {
	case RoleController, RoleInference, RoleApp, RoleStorage:
		return true
	}
	return false
}

// Specs is the hardware a node reports at announce time.
type Specs struct {
	CPUCores  int `json:"cpu_cores"`
	MemoryGB  int `json:"memory_gb"`
	GPUVRAMGB int `json:"gpu_vram_gb"`
}

// Node is a single machine in the pool.
//
// EnrollSecret and Credential are secrets and never serialize (json:"-"):
// EnrollSecret proves a credential request comes from the machine that
// announced; Credential authenticates heartbeats afterward.
type Node struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Site          string          `json:"site"` // the site (location) this node belongs to
	Class         Class           `json:"class"`
	Role          Role            `json:"role,omitempty"`
	State         State           `json:"state"`
	Specs         Specs           `json:"specs"`
	PairingCode   string          `json:"pairing_code,omitempty"`
	Secured       bool            `json:"secured"` // has completed an mTLS handshake with a CA-signed cert
	Metrics       *metrics.Sample `json:"metrics,omitempty"`
	EnrollSecret  string          `json:"-"`
	Credential    string          `json:"-"`
	LastHeartbeat time.Time       `json:"last_heartbeat,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

// Site is a location grouping nodes (à la UniFi sites). Multi-site aware from
// day one; cross-site connectivity is assumed (e.g. UniFi SD-WAN) for now.
type Site struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Errors returned by Store implementations.
var (
	ErrNotFound = errors.New("node not found")
	ErrExists   = errors.New("node already exists")
)

// Store is the persistence contract for node inventory and sites.
// Implementations must be safe for concurrent use and must return copies, never
// internal pointers, so callers can mutate freely and commit with Update.
type Store interface {
	Create(n *Node) error
	Get(id string) (*Node, error)
	Update(n *Node) error
	// Mutate atomically applies fn to the stored node while holding the write
	// lock, persisting the change only if fn returns true. This makes a
	// read-modify-write race-free (e.g. the reaper re-checking liveness can't
	// clobber a heartbeat that landed mid-sweep). Returns ErrNotFound if absent.
	Mutate(id string, fn func(*Node) bool) error
	Delete(id string) error
	List() ([]*Node, error)

	// Sites
	PutSite(s *Site) error          // create or update (idempotent)
	GetSite(id string) (*Site, error)
	ListSites() ([]*Site, error)

	// Meta is a small KV for controller state that isn't node inventory (e.g.
	// the CA root). Keeping it in the store means it persists — and later
	// replicates — through the same pluggable layer as everything else.
	GetMeta(key string) ([]byte, error) // ErrNotFound if absent
	PutMeta(key string, value []byte) error
}

// Memory is an in-memory Store. Zero value is not usable; use NewMemory.
type Memory struct {
	mu    sync.RWMutex
	nodes map[string]Node
	sites map[string]Site
	meta  map[string][]byte
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{nodes: make(map[string]Node), sites: make(map[string]Site), meta: make(map[string][]byte)}
}

// GetMeta returns a copy of the value for key, or ErrNotFound.
func (m *Memory) GetMeta(key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.meta[key]
	if !ok {
		return nil, ErrNotFound
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

// PutMeta stores a copy of value under key (create or update).
func (m *Memory) PutMeta(key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := make([]byte, len(value))
	copy(v, value)
	m.meta[key] = v
	return nil
}

// PutSite creates or updates a site (idempotent).
func (m *Memory) PutSite(s *Site) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sites[s.ID] = *s
	return nil
}

// GetSite returns a copy of the site, or ErrNotFound.
func (m *Memory) GetSite(id string) (*Site, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sites[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := s
	return &c, nil
}

// ListSites returns copies of all sites, sorted by ID.
func (m *Memory) ListSites() ([]*Site, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Site, 0, len(m.sites))
	for _, s := range m.sites {
		c := s
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Create inserts n. It returns ErrExists if the ID is already present.
func (m *Memory) Create(n *Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[n.ID]; ok {
		return ErrExists
	}
	m.nodes[n.ID] = *n
	return nil
}

// Get returns a copy of the node, or ErrNotFound.
func (m *Memory) Get(id string) (*Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	n, ok := m.nodes[id]
	if !ok {
		return nil, ErrNotFound
	}
	c := n
	return &c, nil
}

// Update overwrites an existing node. It returns ErrNotFound if absent.
func (m *Memory) Update(n *Node) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[n.ID]; !ok {
		return ErrNotFound
	}
	m.nodes[n.ID] = *n
	return nil
}

// Mutate atomically applies fn under the write lock. See Store.Mutate.
func (m *Memory) Mutate(id string, fn func(*Node) bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.nodes[id]
	if !ok {
		return ErrNotFound
	}
	c := n
	if fn(&c) {
		m.nodes[id] = c
	}
	return nil
}

// Delete removes a node. It returns ErrNotFound if absent.
func (m *Memory) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.nodes[id]; !ok {
		return ErrNotFound
	}
	delete(m.nodes, id)
	return nil
}

// List returns copies of all nodes, sorted by creation time then ID for a
// stable order.
func (m *Memory) List() ([]*Node, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Node, 0, len(m.nodes))
	for _, n := range m.nodes {
		c := n
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
