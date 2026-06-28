package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/traego/traego/internal/api"
	"github.com/traego/traego/internal/store"
)

// testController spins up a real controller behind httptest so the agent
// exercises the actual API, not a mock.
type testController struct {
	url   string
	srv   *api.Server
	admin string
	http  *http.Client
}

func newController(t *testing.T) *testController {
	t.Helper()
	srv, err := api.New(api.Config{
		Store:            store.NewMemory(),
		AdminKey:         "admin-key",
		JoinToken:        "join-tok",
		HeartbeatTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &testController{url: ts.URL, srv: srv, admin: "admin-key", http: ts.Client()}
}

func (c *testController) adopt(t *testing.T, id, code string, role store.Role) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"pairing_code": code, "role": string(role)})
	req, _ := http.NewRequest("POST", c.url+"/api/v1/nodes/"+id+"/adopt", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.admin)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt: status %d", resp.StatusCode)
	}
}

func (c *testController) node(t *testing.T, id string) store.Node {
	t.Helper()
	req, _ := http.NewRequest("GET", c.url+"/api/v1/nodes/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+c.admin)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	defer resp.Body.Close()
	var n store.Node
	_ = json.NewDecoder(resp.Body).Decode(&n)
	return n
}

func (c *testController) listOne(t *testing.T) (store.Node, bool) {
	t.Helper()
	req, _ := http.NewRequest("GET", c.url+"/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+c.admin)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Nodes []store.Node `json:"nodes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Nodes) == 0 {
		return store.Node{}, false
	}
	return out.Nodes[0], true
}

func (c *testController) delete(t *testing.T, id string) {
	t.Helper()
	req, _ := http.NewRequest("DELETE", c.url+"/api/v1/nodes/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+c.admin)
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	resp.Body.Close()
}

func newAgent(c *testController, name string) *Agent {
	return New(Config{
		ControllerURL:     c.url,
		JoinToken:         "join-tok",
		Name:              name,
		Specs:             store.Specs{CPUCores: 8, GPUVRAMGB: 24},
		HeartbeatInterval: 20 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
		HTTPClient:        c.http,
	})
}

func TestAgentAnnounceThenAdoptThenHeartbeat(t *testing.T) {
	c := newController(t)
	a := newAgent(c, "rack-gpu-01")
	ctx := context.Background()

	if err := a.Announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if a.ID() == "" || a.PairingCode() == "" {
		t.Fatalf("announce did not populate id/code")
	}

	c.adopt(t, a.ID(), a.PairingCode(), store.RoleInference)

	if err := a.AwaitCredential(ctx); err != nil {
		t.Fatalf("await credential: %v", err)
	}
	if a.Credential() == "" || a.Role() != store.RoleInference {
		t.Fatalf("credential/role not set: cred=%q role=%q", a.Credential(), a.Role())
	}

	if err := a.Heartbeat(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := c.node(t, a.ID()); got.State != store.StateOnline {
		t.Fatalf("want online, got %s", got.State)
	}

	if err := a.Leave(ctx); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if got := c.node(t, a.ID()); got.State != store.StateOffline {
		t.Fatalf("want offline, got %s", got.State)
	}
}

func TestAgentAwaitCredentialBlocksUntilAdopted(t *testing.T) {
	c := newController(t)
	a := newAgent(c, "slow-adopt")
	ctx := context.Background()
	if err := a.Announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- a.AwaitCredential(ctx) }()

	// should still be blocking
	select {
	case err := <-done:
		t.Fatalf("AwaitCredential returned before adoption: %v", err)
	case <-time.After(40 * time.Millisecond):
	}

	c.adopt(t, a.ID(), a.PairingCode(), store.RoleApp)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("await credential: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("AwaitCredential did not return after adoption")
	}
}

func TestAgentRunFullLifecycle(t *testing.T) {
	c := newController(t)
	a := newAgent(c, "lifecycle-node")
	ctx, cancel := context.WithCancel(context.Background())

	runErr := make(chan error, 1)
	go func() { runErr <- a.Run(ctx) }()

	// wait for the node to appear, then adopt it using its pairing code
	var n store.Node
	if !waitFor(t, time.Second, func() bool {
		var ok bool
		n, ok = c.listOne(t)
		return ok && n.PairingCode != ""
	}) {
		t.Fatal("node never announced")
	}
	c.adopt(t, n.ID, n.PairingCode, store.RoleInference)

	if !waitFor(t, time.Second, func() bool { return c.node(t, n.ID).State == store.StateOnline }) {
		t.Fatal("node never came online")
	}

	cancel()
	if err := <-runErr; err != context.Canceled {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
	// graceful Leave on shutdown
	if !waitFor(t, time.Second, func() bool { return c.node(t, n.ID).State == store.StateOffline }) {
		t.Fatal("node did not drain to offline on shutdown")
	}
}

func TestAgentAnnounceBadToken(t *testing.T) {
	c := newController(t)
	a := New(Config{ControllerURL: c.url, JoinToken: "wrong", Name: "x", HTTPClient: c.http})
	if err := a.Announce(context.Background()); err == nil {
		t.Fatal("expected announce to fail with bad join token")
	}
}

func TestAgentHeartbeatAndLeaveRejectedWithoutCredential(t *testing.T) {
	c := newController(t)
	a := newAgent(c, "no-cred")
	ctx := context.Background()
	if err := a.Announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	// never adopted -> empty credential -> controller returns 401
	if err := a.Heartbeat(ctx); err == nil {
		t.Fatal("expected heartbeat to fail without a credential")
	}
	if err := a.Leave(ctx); err == nil {
		t.Fatal("expected leave to fail without a credential")
	}
}

func TestAgentAwaitCredentialUnexpectedStatus(t *testing.T) {
	c := newController(t)
	a := newAgent(c, "vanishing")
	ctx := context.Background()
	if err := a.Announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	c.delete(t, a.ID()) // node disappears -> credential endpoint 404
	if err := a.AwaitCredential(ctx); err == nil {
		t.Fatal("expected AwaitCredential to error on 404")
	}
}

func TestAgentNetworkError(t *testing.T) {
	a := New(Config{
		ControllerURL: "http://127.0.0.1:1", // connection refused
		JoinToken:     "join-tok",
		Name:          "offline",
		HTTPClient:    &http.Client{Timeout: time.Second},
	})
	if err := a.Announce(context.Background()); err == nil {
		t.Fatal("expected a network error")
	}
}

func TestDetectSpecs(t *testing.T) {
	t.Setenv("TRAEGO_MEMORY_GB", "64")
	t.Setenv("TRAEGO_GPU_VRAM_GB", "24")
	s := DetectSpecs()
	if s.CPUCores < 1 {
		t.Fatalf("cpu cores not detected: %d", s.CPUCores)
	}
	if s.MemoryGB != 64 || s.GPUVRAMGB != 24 {
		t.Fatalf("env specs not applied: %+v", s)
	}
}

// ---- helpers ----

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
