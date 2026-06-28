package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func newBolt(t *testing.T) *Bolt {
	t.Helper()
	b, err := OpenBolt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenBolt: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

// runStoreContract exercises any Store implementation identically.
func runStoreContract(t *testing.T, s Store) {
	t.Helper()
	// nodes: create/get/update/mutate/delete/list
	if err := s.Create(sample("a")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.Create(sample("a")); !errors.Is(err, ErrExists) {
		t.Fatalf("dup create: want ErrExists, got %v", err)
	}
	got, err := s.Get("a")
	if err != nil || got.Name != "node-a" {
		t.Fatalf("get: %+v %v", got, err)
	}
	got.State = StateOnline
	if err := s.Update(got); err != nil {
		t.Fatalf("update: %v", err)
	}
	if g, _ := s.Get("a"); g.State != StateOnline {
		t.Fatalf("update not persisted")
	}
	if err := s.Mutate("a", func(n *Node) bool { n.Role = RoleInference; return true }); err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if g, _ := s.Get("a"); g.Role != RoleInference {
		t.Fatalf("mutate not persisted")
	}
	if err := s.Mutate("a", func(n *Node) bool { n.Role = RoleApp; return false }); err != nil {
		t.Fatalf("mutate-false: %v", err)
	}
	if g, _ := s.Get("a"); g.Role != RoleInference {
		t.Fatalf("mutate(false) should not persist")
	}
	if _, err := s.Get("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: want ErrNotFound, got %v", err)
	}
	_ = s.Create(sample("b"))
	if list, _ := s.List(); len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	if err := s.Delete("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get("a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted node still present")
	}

	// sites
	if err := s.PutSite(&Site{ID: "hq", Name: "Headquarters"}); err != nil {
		t.Fatalf("putsite: %v", err)
	}
	if err := s.PutSite(&Site{ID: "hq", Name: "HQ"}); err != nil { // idempotent update
		t.Fatalf("putsite update: %v", err)
	}
	site, err := s.GetSite("hq")
	if err != nil || site.Name != "HQ" {
		t.Fatalf("getsite: %+v %v", site, err)
	}
	if _, err := s.GetSite("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("getsite missing: want ErrNotFound, got %v", err)
	}
	_ = s.PutSite(&Site{ID: "branch", Name: "Branch"})
	if sites, _ := s.ListSites(); len(sites) != 2 || sites[0].ID != "branch" {
		t.Fatalf("listsites wrong: %+v", sites)
	}
}

func TestMemoryContract(t *testing.T) { runStoreContract(t, NewMemory()) }
func TestBoltContract(t *testing.T)   { runStoreContract(t, newBolt(t)) }

// TestBoltPersistsAcrossReopen is the whole point: data (including the
// json:"-" Credential secret) survives a close/reopen.
func TestBoltPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")

	b1, err := OpenBolt(path)
	if err != nil {
		t.Fatal(err)
	}
	n := sample("keep")
	n.State = StateOnline
	n.Credential = "secret-cred-123" // json:"-", must still persist via gob
	n.Site = "branch"
	_ = b1.Create(n)
	_ = b1.PutSite(&Site{ID: "branch", Name: "Branch Office"})
	b1.Close()

	b2, err := OpenBolt(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer b2.Close()

	got, err := b2.Get("keep")
	if err != nil {
		t.Fatalf("node lost across restart: %v", err)
	}
	if got.State != StateOnline || got.Site != "branch" {
		t.Fatalf("node fields not persisted: %+v", got)
	}
	if got.Credential != "secret-cred-123" {
		t.Fatalf("credential (json:\"-\") not persisted: %q — node couldn't re-auth after restart", got.Credential)
	}
	if s, err := b2.GetSite("branch"); err != nil || s.Name != "Branch Office" {
		t.Fatalf("site not persisted: %+v %v", s, err)
	}
}
