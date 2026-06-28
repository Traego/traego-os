package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/traego/traego/internal/api"
	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/store"
)

// TestAgentSecureMode runs the full app-module flow: announce -> adopt ->
// fetch a CA-signed cert -> establish mTLS -> heartbeat over mutual TLS, against
// a real TLS listener that requires and verifies client certs.
func TestAgentSecureMode(t *testing.T) {
	caObj, err := ca.New()
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	st := store.NewMemory()
	srv, err := api.New(api.Config{
		Store: st, AdminKey: "admin-key", JoinToken: "join-tok",
		HeartbeatTimeout: time.Minute, CA: caObj,
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	serverCert, err := caObj.ServerCertificate([]string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}
	secSrv := httptest.NewUnstartedServer(srv.SecureHandler())
	secSrv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    caObj.Pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	secSrv.StartTLS()
	defer secSrv.Close()

	a := New(Config{
		ControllerURL:     httpSrv.URL,
		SecureURL:         secSrv.URL,
		JoinToken:         "join-tok",
		Name:              "app-module",
		Specs:             store.Specs{CPUCores: 4},
		Secure:            true,
		HeartbeatInterval: 20 * time.Millisecond,
		PollInterval:      10 * time.Millisecond,
		HTTPClient:        httpSrv.Client(),
	})
	ctx := context.Background()

	if err := a.Announce(ctx); err != nil {
		t.Fatalf("announce: %v", err)
	}
	adoptAdmin(t, httpSrv, a.ID(), a.PairingCode(), "app")
	if err := a.AwaitCredential(ctx); err != nil {
		t.Fatalf("await credential: %v", err)
	}
	if err := a.GoSecure(ctx); err != nil {
		t.Fatalf("GoSecure: %v", err)
	}
	if !a.Secured() {
		t.Fatal("agent reports not secured after GoSecure")
	}
	if err := a.Heartbeat(ctx); err != nil {
		t.Fatalf("secure heartbeat: %v", err)
	}

	n := getNodeAdmin(t, httpSrv, a.ID())
	if n.State != store.StateOnline || !n.Secured {
		t.Fatalf("node not online+secured after mTLS heartbeat: %+v", n)
	}
}

func adoptAdmin(t *testing.T, srv *httptest.Server, id, code, role string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"pairing_code": code, "role": role})
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/nodes/"+id+"/adopt", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer admin-key")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("adopt: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt status %d", resp.StatusCode)
	}
}

func getNodeAdmin(t *testing.T, srv *httptest.Server, id string) store.Node {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/nodes/"+id, nil)
	req.Header.Set("Authorization", "Bearer admin-key")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	defer resp.Body.Close()
	var n store.Node
	_ = json.NewDecoder(resp.Body).Decode(&n)
	return n
}
