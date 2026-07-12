package agent

import (
	"context"
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/traego/traego/internal/api"
	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/store"
)

// tlsController stands up an https control plane with a CA-minted server cert,
// the way the real controller serves it (TLS by default).
func tlsController(t *testing.T) (*httptest.Server, *ca.CA) {
	t.Helper()
	caObj, err := ca.New()
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	srv, err := api.New(api.Config{
		Store: store.NewMemory(), AdminKey: "admin-key", JoinToken: "join-tok",
		HeartbeatTimeout: time.Minute, CA: caObj,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	serverCert, err := caObj.ServerCertificate([]string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	ts := httptest.NewUnstartedServer(srv.Handler())
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{serverCert}}
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts, caObj
}

// TestAgentPinsControllerCA: with the right fingerprint the agent talks to the
// TLS controller; with a wrong one it refuses — the join cannot be intercepted.
func TestAgentPinsControllerCA(t *testing.T) {
	ts, caObj := tlsController(t)

	good := New(Config{
		ControllerURL: ts.URL, JoinToken: "join-tok", Name: "pinned",
		CAFingerprint: caObj.Fingerprint(),
	})
	if err := good.Announce(context.Background()); err != nil {
		t.Fatalf("announce with correct pin: %v", err)
	}

	bad := New(Config{
		ControllerURL: ts.URL, JoinToken: "join-tok", Name: "wrong-pin",
		CAFingerprint: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
	})
	if err := bad.Announce(context.Background()); err == nil {
		t.Fatal("announce succeeded against a controller whose CA does not match the pin")
	}
}

// TestAgentTOFUWithoutPin: with no fingerprint configured the agent trusts the
// CA on first use (warning logged) and still completes the join.
func TestAgentTOFUWithoutPin(t *testing.T) {
	ts, _ := tlsController(t)
	var warned bool
	a := New(Config{
		ControllerURL: ts.URL, JoinToken: "join-tok", Name: "tofu",
		Logf: func(format string, _ ...any) {
			if len(format) >= 7 && format[:7] == "WARNING" {
				warned = true
			}
		},
	})
	if err := a.Announce(context.Background()); err != nil {
		t.Fatalf("announce over TOFU: %v", err)
	}
	if !warned {
		t.Fatal("TOFU happened silently — must warn the operator")
	}
}
