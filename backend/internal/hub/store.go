package hub

import (
	"encoding/json"
	"errors"
	"sync"

	bolt "go.etcd.io/bbolt"
)

// ErrNotFound mirrors the controller store's sentinel for absent entries.
var ErrNotFound = errors.New("hub: not found")

var bucketControllers = []byte("controllers")

// Bolt persists the registry in its own bbolt file (hub.db).
type Bolt struct{ db *bolt.DB }

// OpenBolt opens (or creates) the hub registry at path.
func OpenBolt(path string) (*Bolt, error) {
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketControllers)
		return err
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Bolt{db: db}, nil
}

// Close releases the underlying database.
func (b *Bolt) Close() error { return b.db.Close() }

// PutController upserts a registry entry.
func (b *Bolt) PutController(c *Controller) error {
	buf, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return b.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketControllers).Put([]byte(c.ID), buf)
	})
}

// GetController returns a copy of the entry, or ErrNotFound.
func (b *Bolt) GetController(id string) (*Controller, error) {
	var c *Controller
	err := b.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketControllers).Get([]byte(id))
		if raw == nil {
			return ErrNotFound
		}
		c = &Controller{}
		return json.Unmarshal(raw, c)
	})
	return c, err
}

// ListControllers returns copies of all entries.
func (b *Bolt) ListControllers() ([]*Controller, error) {
	var out []*Controller
	err := b.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucketControllers).ForEach(func(_, raw []byte) error {
			c := &Controller{}
			if err := json.Unmarshal(raw, c); err != nil {
				return err
			}
			out = append(out, c)
			return nil
		})
	})
	return out, err
}

// Memory is an in-memory Store for tests.
type Memory struct {
	mu sync.Mutex
	m  map[string]*Controller
}

// NewMemory returns an empty in-memory registry.
func NewMemory() *Memory { return &Memory{m: map[string]*Controller{}} }

// PutController upserts a copy of c.
func (m *Memory) PutController(c *Controller) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *c
	m.m[c.ID] = &cp
	return nil
}

// GetController returns a copy, or ErrNotFound.
func (m *Memory) GetController(id string) (*Controller, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.m[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

// ListControllers returns copies of all entries.
func (m *Memory) ListControllers() ([]*Controller, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Controller, 0, len(m.m))
	for _, c := range m.m {
		cp := *c
		out = append(out, &cp)
	}
	return out, nil
}
