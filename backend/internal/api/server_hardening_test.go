package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/traego/traego/internal/store"
)

// adoptAndCredential walks a node through announce -> adopt -> credential.
func adoptAndCredential(t *testing.T, h *harness) (announceResp, credentialResp) {
	t.Helper()
	a := h.announce(t, "node")
	if rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt",
		adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("adopt: %d", rec.Code)
	}
	var cr credentialResp
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("credential: %d", rec.Code)
	}
	mustJSON(t, rec, &cr)
	return a, cr
}

// TestEnrollSecretIsOneShot: once the credential is handed out, the enroll
// secret must be spent — it cannot serve as a second permanent credential.
func TestEnrollSecretIsOneShot(t *testing.T) {
	h := newHarness(t)
	a, cr := adoptAndCredential(t, h)
	if cr.Credential == "" {
		t.Fatal("no credential issued")
	}
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("replayed enroll secret: want 403, got %d", rec.Code)
	}
	if n, _ := h.st.Get(a.ID); n.EnrollSecret != "" {
		t.Fatalf("enroll secret still stored after use")
	}
}

// TestBearerHeartbeatClearsSecured: a node that heartbeats on the bearer plane
// is not on mTLS right now, so a stale Secured badge must be cleared.
func TestBearerHeartbeatClearsSecured(t *testing.T) {
	h := newHarness(t)
	a, cr := adoptAndCredential(t, h)
	_ = h.st.Mutate(a.ID, func(n *store.Node) bool { n.Secured = true; return true })
	if rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential)); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d", rec.Code)
	}
	if n, _ := h.st.Get(a.ID); n.Secured {
		t.Fatal("Secured still true after a bearer (non-mTLS) heartbeat")
	}
}

// TestAuthFailureRateLimit: repeated bad admin keys from one IP get cut off
// with 429 before any secret comparison, and recover after the window.
func TestAuthFailureRateLimit(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < authFailLimit; i++ {
		if rec := h.do("GET", "/api/v1/nodes", nil, bearerHdr("wrong-key")); rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: want 401, got %d", i, rec.Code)
		}
	}
	// budget exhausted: even a CORRECT key is refused until the window passes,
	// and the join-token path shares the same per-IP budget
	if rec := h.do("GET", "/api/v1/nodes", nil, adminHdr); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: want 429, got %d", authFailLimit, rec.Code)
	}
	if rec := h.do("POST", "/api/v1/discovery/announce", announceReq{Name: "x"}, joinHdr); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("announce after failures: want 429, got %d", rec.Code)
	}
	// window expires -> allowed again
	h.clock = h.clock.Add(authFailWindow + time.Second)
	if rec := h.do("GET", "/api/v1/nodes", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("after window: want 200, got %d", rec.Code)
	}
}

// TestAuthFailuresAreLogged: guessing attempts must be visible to operators.
func TestAuthFailuresAreLogged(t *testing.T) {
	st := store.NewMemory()
	var logged []string
	srv, err := New(Config{
		Store: st, AdminKey: "admin-key", JoinToken: "join-tok",
		Logf: func(format string, args ...any) { logged = append(logged, format) },
	})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{st: st, srv: srv, h: srv.Handler(), clock: time.Now()}
	h.do("GET", "/api/v1/nodes", nil, bearerHdr("wrong"))
	if len(logged) == 0 {
		t.Fatal("auth failure was not logged")
	}
}
