package store

import (
	"errors"
	"testing"
	"time"
)

func sample(id string) *Node {
	return &Node{
		ID:        id,
		Name:      "node-" + id,
		Class:     ClassPersistent,
		State:     StatePending,
		Specs:     Specs{CPUCores: 8, MemoryGB: 32, GPUVRAMGB: 24},
		CreatedAt: time.Unix(1000, 0),
	}
}

func TestMemoryCreateAndGet(t *testing.T) {
	m := NewMemory()
	if err := m.Create(sample("a")); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := m.Get("a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "node-a" || got.Specs.GPUVRAMGB != 24 {
		t.Fatalf("unexpected node: %+v", got)
	}
}

func TestMemoryCreateDuplicate(t *testing.T) {
	m := NewMemory()
	_ = m.Create(sample("a"))
	if err := m.Create(sample("a")); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
}

func TestMemoryGetMissing(t *testing.T) {
	m := NewMemory()
	if _, err := m.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// Get must return a copy: mutating the result must not affect the store.
func TestMemoryGetReturnsCopy(t *testing.T) {
	m := NewMemory()
	_ = m.Create(sample("a"))
	got, _ := m.Get("a")
	got.Name = "tampered"
	fresh, _ := m.Get("a")
	if fresh.Name != "node-a" {
		t.Fatalf("store was mutated through returned pointer: %q", fresh.Name)
	}
}

func TestMemoryUpdate(t *testing.T) {
	m := NewMemory()
	_ = m.Create(sample("a"))
	n, _ := m.Get("a")
	n.State = StateOnline
	if err := m.Update(n); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _ := m.Get("a")
	if got.State != StateOnline {
		t.Fatalf("update not persisted: %s", got.State)
	}
}

func TestMemoryUpdateMissing(t *testing.T) {
	m := NewMemory()
	if err := m.Update(sample("ghost")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemoryDelete(t *testing.T) {
	m := NewMemory()
	_ = m.Create(sample("a"))
	if err := m.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("node still present after delete")
	}
	if err := m.Delete("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound deleting twice, got %v", err)
	}
}

func TestMemoryListIsSortedAndCopied(t *testing.T) {
	m := NewMemory()
	a := sample("a")
	a.CreatedAt = time.Unix(2000, 0)
	b := sample("b")
	b.CreatedAt = time.Unix(1000, 0)
	_ = m.Create(a)
	_ = m.Create(b)

	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].ID != "b" || list[1].ID != "a" {
		t.Fatalf("want [b a] by CreatedAt, got %v", []string{list[0].ID, list[1].ID})
	}
	// mutating a list entry must not affect the store
	list[0].Name = "tampered"
	fresh, _ := m.Get("b")
	if fresh.Name == "tampered" {
		t.Fatalf("List returned internal pointer")
	}
}

func TestMemoryMutate(t *testing.T) {
	m := NewMemory()
	_ = m.Create(sample("a"))

	// fn returns true -> change persists
	if err := m.Mutate("a", func(n *Node) bool { n.State = StateOnline; return true }); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if got, _ := m.Get("a"); got.State != StateOnline {
		t.Fatalf("mutate did not persist: %s", got.State)
	}

	// fn returns false -> change discarded
	_ = m.Mutate("a", func(n *Node) bool { n.State = StateOffline; return false })
	if got, _ := m.Get("a"); got.State != StateOnline {
		t.Fatalf("mutate persisted despite returning false: %s", got.State)
	}

	// missing node
	if err := m.Mutate("ghost", func(n *Node) bool { return true }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClassAndRoleValidation(t *testing.T) {
	if !ClassPersistent.Valid() || !ClassEphemeral.Valid() || Class("weird").Valid() {
		t.Fatal("Class.Valid wrong")
	}
	for _, r := range []Role{RoleController, RoleInference, RoleApp, RoleStorage} {
		if !ValidRole(r) {
			t.Fatalf("role %s should be valid", r)
		}
	}
	if ValidRole("garbage") {
		t.Fatal("garbage role should be invalid")
	}
}
