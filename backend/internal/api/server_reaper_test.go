package api

import (
	"testing"
	"time"

	"github.com/traego/traego/internal/store"
)

// staleListStore makes List() report every node as online-and-stale, while
// Mutate/Get see the real underlying state. It simulates the race the reaper
// must survive: a heartbeat lands after the reaper takes its List() snapshot.
type staleListStore struct {
	*store.Memory
}

func (s staleListStore) List() ([]*store.Node, error) {
	nodes, err := s.Memory.List()
	for _, n := range nodes {
		n.State = store.StateOnline
		n.LastHeartbeat = time.Unix(0, 0) // ancient -> looks reapable
	}
	return nodes, err
}

func TestReaperDoesNotClobberFreshHeartbeat(t *testing.T) {
	mem := store.NewMemory()
	now := time.Unix(1_700_000_000, 0)
	srv, err := New(Config{
		Store: staleListStore{mem}, AdminKey: "k", JoinToken: "j",
		HeartbeatTimeout: 30 * time.Second,
		Now:              func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	// the REAL node is online and just heartbeated (fresh)
	_ = mem.Create(&store.Node{ID: "n1", State: store.StateOnline, LastHeartbeat: now})

	// List() lies that it's stale, but Mutate re-checks the real fresh node
	if reaped := srv.ReapOffline(); reaped != 0 {
		t.Fatalf("reaper clobbered a fresh node: reaped %d", reaped)
	}
	if n, _ := mem.Get("n1"); n.State != store.StateOnline {
		t.Fatalf("node flipped to %s despite a fresh heartbeat", n.State)
	}

	// a genuinely stale node IS reaped
	_ = mem.Create(&store.Node{ID: "n2", State: store.StateOnline, LastHeartbeat: now.Add(-time.Hour)})
	if reaped := srv.ReapOffline(); reaped != 1 {
		t.Fatalf("want 1 stale node reaped, got %d", reaped)
	}
	if n, _ := mem.Get("n2"); n.State != store.StateOffline {
		t.Fatalf("stale node not reaped: %s", n.State)
	}
}
