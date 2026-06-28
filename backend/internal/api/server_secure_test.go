package api

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/store"
)

func secureHarness(t *testing.T) (*harness, *ca.CA) {
	t.Helper()
	caObj, err := ca.New()
	if err != nil {
		t.Fatalf("ca.New: %v", err)
	}
	st := store.NewMemory()
	h := &harness{st: st, clock: time.Unix(1_700_000_000, 0)}
	var idN, secN, codeN int
	srv, err := New(Config{
		Store: st, AdminKey: "admin-key", JoinToken: "join-tok",
		HeartbeatTimeout: 30 * time.Second, CA: caObj,
		NewID:     func() string { idN++; return fmt.Sprintf("n-%d", idN) },
		NewSecret: func() string { secN++; return fmt.Sprintf("secret-%d", secN) },
		NewCode:   func() string { codeN++; return fmt.Sprintf("CODE-%d", codeN) },
		Now:       func() time.Time { return h.clock },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.srv = srv
	h.h = srv.Handler()
	return h, caObj
}

// adoptedNode walks announce->adopt->credential and returns the id + credential.
func adoptedNode(t *testing.T, h *harness) (string, string) {
	t.Helper()
	a := h.announce(t, "secure-node")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)
	var cr credentialResp
	mustJSON(t, h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil), &cr)
	return a.ID, cr.Credential
}

func (h *harness) secureReq(method, path, cn string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cn != "" {
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: cn}}},
		}
	}
	rec := httptest.NewRecorder()
	h.srv.SecureHandler().ServeHTTP(rec, req)
	return rec
}

func TestCAEndpoint(t *testing.T) {
	h, _ := secureHarness(t)
	rec := h.do("GET", "/api/v1/ca", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ca: %d", rec.Code)
	}
	if block, _ := pem.Decode(rec.Body.Bytes()); block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("ca endpoint did not return a CERTIFICATE PEM")
	}
}

func TestCAEndpointAbsentWithoutCA(t *testing.T) {
	h := newHarness(t) // no CA configured
	if rec := h.do("GET", "/api/v1/ca", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when CA disabled, got %d", rec.Code)
	}
}

func TestCertificateIssuance(t *testing.T) {
	h, caObj := secureHarness(t)
	id, cred := adoptedNode(t, h)

	_, csr, err := ca.NewKeyAndCSR("ignored-cn")
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	rec := h.do("POST", "/api/v1/nodes/"+id+"/certificate", string(csr), bearerHdr(cred))
	if rec.Code != http.StatusOK {
		t.Fatalf("certificate: %d (%s)", rec.Code, rec.Body)
	}
	var cr certResp
	mustJSON(t, rec, &cr)
	block, _ := pem.Decode([]byte(cr.Certificate))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse issued cert: %v", err)
	}
	if leaf.Subject.CommonName != id {
		t.Fatalf("issued CN = %q, want node id %q", leaf.Subject.CommonName, id)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: caObj.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("issued cert does not chain to CA: %v", err)
	}
}

func TestCertificateRequiresCredential(t *testing.T) {
	h, _ := secureHarness(t)
	id, _ := adoptedNode(t, h)
	_, csr, _ := ca.NewKeyAndCSR("x")
	if rec := h.do("POST", "/api/v1/nodes/"+id+"/certificate", string(csr), bearerHdr("wrong")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestCertificateBadCSR(t *testing.T) {
	h, _ := secureHarness(t)
	id, cred := adoptedNode(t, h)
	if rec := h.do("POST", "/api/v1/nodes/"+id+"/certificate", "garbage", bearerHdr(cred)); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestSecureHeartbeat(t *testing.T) {
	h, _ := secureHarness(t)
	id, _ := adoptedNode(t, h)

	rec := h.secureReq("POST", "/api/v1/nodes/"+id+"/heartbeat", id)
	if rec.Code != http.StatusOK {
		t.Fatalf("secure heartbeat: %d (%s)", rec.Code, rec.Body)
	}
	n, _ := h.st.Get(id)
	if n.State != store.StateOnline || !n.Secured {
		t.Fatalf("node not online+secured: state=%s secured=%v", n.State, n.Secured)
	}
}

func TestSecureHeartbeatRejections(t *testing.T) {
	h, _ := secureHarness(t)
	id, _ := adoptedNode(t, h)

	// no client cert
	if rec := h.secureReq("POST", "/api/v1/nodes/"+id+"/heartbeat", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-cert: want 401, got %d", rec.Code)
	}
	// cert identity does not match the path id
	if rec := h.secureReq("POST", "/api/v1/nodes/"+id+"/heartbeat", "n-someone-else"); rec.Code != http.StatusForbidden {
		t.Fatalf("mismatch: want 403, got %d", rec.Code)
	}
}

func TestSecureWhoami(t *testing.T) {
	h, _ := secureHarness(t)
	if rec := h.secureReq("GET", "/api/v1/secure/whoami", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-cert whoami: want 401, got %d", rec.Code)
	}
	rec := h.secureReq("GET", "/api/v1/secure/whoami", "n-xyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("whoami: %d", rec.Code)
	}
	var out struct {
		NodeID  string `json:"node_id"`
		Secured bool   `json:"secured"`
	}
	mustJSON(t, rec, &out)
	if out.NodeID != "n-xyz" || !out.Secured {
		t.Fatalf("whoami wrong: %+v", out)
	}
}
