package store

import (
	"bytes"
	"encoding/gob"
	"sort"
	"time"

	"go.etcd.io/bbolt"
)

// Bolt is a persistent Store backed by bbolt (a single embedded file). It uses
// gob so unexported-from-JSON secrets (EnrollSecret, Credential) still persist —
// json:"-" hides them from the API, not from disk. bbolt's single-writer Update
// transaction makes Mutate atomic for free.
type Bolt struct {
	db *bbolt.DB
}

var (
	nodesBucket = []byte("nodes")
	sitesBucket = []byte("sites")
	metaBucket  = []byte("meta")
)

// OpenBolt opens (or creates) the database file at path.
func OpenBolt(path string) (*Bolt, error) {
	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		for _, b := range [][]byte{nodesBucket, sitesBucket, metaBucket} {
			if _, e := tx.CreateBucketIfNotExists(b); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &Bolt{db: db}, nil
}

// Close releases the database file.
func (b *Bolt) Close() error { return b.db.Close() }

func gobEncode(v any) ([]byte, error) {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(v)
	return buf.Bytes(), err
}

func (b *Bolt) Create(n *Node) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		bk := tx.Bucket(nodesBucket)
		if bk.Get([]byte(n.ID)) != nil {
			return ErrExists
		}
		data, err := gobEncode(n)
		if err != nil {
			return err
		}
		return bk.Put([]byte(n.ID), data)
	})
}

func (b *Bolt) Get(id string) (*Node, error) {
	var n Node
	err := b.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(nodesBucket).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return gob.NewDecoder(bytes.NewReader(data)).Decode(&n)
	})
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (b *Bolt) Update(n *Node) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		bk := tx.Bucket(nodesBucket)
		if bk.Get([]byte(n.ID)) == nil {
			return ErrNotFound
		}
		data, err := gobEncode(n)
		if err != nil {
			return err
		}
		return bk.Put([]byte(n.ID), data)
	})
}

func (b *Bolt) Mutate(id string, fn func(*Node) bool) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		bk := tx.Bucket(nodesBucket)
		data := bk.Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		var n Node
		if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&n); err != nil {
			return err
		}
		if !fn(&n) {
			return nil
		}
		out, err := gobEncode(&n)
		if err != nil {
			return err
		}
		return bk.Put([]byte(id), out)
	})
}

func (b *Bolt) Delete(id string) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		bk := tx.Bucket(nodesBucket)
		if bk.Get([]byte(id)) == nil {
			return ErrNotFound
		}
		return bk.Delete([]byte(id))
	})
}

func (b *Bolt) List() ([]*Node, error) {
	var out []*Node
	err := b.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(nodesBucket).ForEach(func(_, v []byte) error {
			var n Node
			if e := gob.NewDecoder(bytes.NewReader(v)).Decode(&n); e != nil {
				return e
			}
			out = append(out, &n)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (b *Bolt) GetMeta(key string) ([]byte, error) {
	var out []byte
	err := b.db.View(func(tx *bbolt.Tx) error {
		v := tx.Bucket(metaBucket).Get([]byte(key))
		if v == nil {
			return ErrNotFound
		}
		out = make([]byte, len(v))
		copy(out, v)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (b *Bolt) PutMeta(key string, value []byte) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(metaBucket).Put([]byte(key), value)
	})
}

func (b *Bolt) PutSite(s *Site) error {
	return b.db.Update(func(tx *bbolt.Tx) error {
		data, err := gobEncode(s)
		if err != nil {
			return err
		}
		return tx.Bucket(sitesBucket).Put([]byte(s.ID), data)
	})
}

func (b *Bolt) GetSite(id string) (*Site, error) {
	var s Site
	err := b.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(sitesBucket).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		return gob.NewDecoder(bytes.NewReader(data)).Decode(&s)
	})
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (b *Bolt) ListSites() ([]*Site, error) {
	var out []*Site
	err := b.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(sitesBucket).ForEach(func(_, v []byte) error {
			var s Site
			if e := gob.NewDecoder(bytes.NewReader(v)).Decode(&s); e != nil {
				return e
			}
			out = append(out, &s)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
