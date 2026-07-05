package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/traego/traego/internal/store"
)

type harness struct {
	srv   *Server
	st    *store.Memory
	h     http.Handler
	clock time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := store.NewMemory()
	h := &harness{st: st, clock: time.Unix(1_700_000_000, 0)}
	var idN, secN, codeN int
	srv, err := New(Config{
		Store:            st,
		AdminKey:         "admin-key",
		JoinToken:        "join-tok",
		HeartbeatTimeout: 30 * time.Second,
		NewID:            func() string { idN++; return fmt.Sprintf("n-%d", idN) },
		NewSecret:        func() string { secN++; return fmt.Sprintf("secret-%d", secN) },
		NewCode:          func() string { codeN++; return fmt.Sprintf("CODE-%d", codeN) },
		Now:              func() time.Time { return h.clock },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.srv = srv
	h.h = srv.Handler()
	return h
}

func (h *harness) do(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case string:
			rdr = strings.NewReader(b)
		default:
			raw, _ := json.Marshal(body)
			rdr = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

var adminHdr = map[string]string{"Authorization": "Bearer admin-key"}
var joinHdr = map[string]string{"X-Traego-Join-Token": "join-tok"}

func (h *harness) announce(t *testing.T, name string) announceResp {
	t.Helper()
	rec := h.do("POST", "/api/v1/discovery/announce",
		announceReq{Name: name, Specs: store.Specs{CPUCores: 8, MemoryGB: 32}}, joinHdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("announce: want 201, got %d (%s)", rec.Code, rec.Body)
	}
	var out announceResp
	mustJSON(t, rec, &out)
	return out
}

func mustJSON(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body, err)
	}
}

func bearerHdr(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

// ---- announce ----

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/healthz", nil, nil); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestAnnounceSuccess(t *testing.T) {
	h := newHarness(t)
	out := h.announce(t, "studio-tower")
	if out.ID == "" || out.EnrollSecret == "" || out.PairingCode == "" {
		t.Fatalf("missing fields: %+v", out)
	}
	if out.State != store.StatePending {
		t.Fatalf("want pending, got %s", out.State)
	}
	n, _ := h.st.Get(out.ID)
	if n.Class != store.ClassPersistent {
		t.Fatalf("default class wrong: %s", n.Class)
	}
}

func TestAnnounceBadJoinToken(t *testing.T) {
	h := newHarness(t)
	for _, tok := range []string{"", "wrong"} {
		hdr := map[string]string{}
		if tok != "" {
			hdr["X-Traego-Join-Token"] = tok
		}
		rec := h.do("POST", "/api/v1/discovery/announce", announceReq{Name: "x"}, hdr)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: want 401, got %d", tok, rec.Code)
		}
	}
}

func TestAnnounceMissingName(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/discovery/announce", announceReq{}, joinHdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestAnnounceInvalidClass(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/discovery/announce",
		announceReq{Name: "x", Class: store.Class("weird")}, joinHdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestAnnounceBadBody(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/discovery/announce", "{not json", joinHdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// ---- full adoption flow ----

func TestAdoptFlowHappyPath(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "rack-gpu-01")

	// adopt
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt",
		adoptReq{PairingCode: a.PairingCode, Role: store.RoleInference}, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("adopt: want 200, got %d (%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "secret-") {
		t.Fatalf("adopt response leaked a secret: %s", rec.Body)
	}

	// trade enroll secret for credential
	rec = h.do("POST", "/api/v1/nodes/"+a.ID+"/credential",
		credentialReq{EnrollSecret: a.EnrollSecret}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("credential: want 200, got %d (%s)", rec.Code, rec.Body)
	}
	var cr credentialResp
	mustJSON(t, rec, &cr)
	if cr.Credential == "" || cr.Role != store.RoleInference {
		t.Fatalf("bad credential resp: %+v", cr)
	}

	// heartbeat -> online
	rec = h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: want 200, got %d (%s)", rec.Code, rec.Body)
	}
	if n, _ := h.st.Get(a.ID); n.State != store.StateOnline {
		t.Fatalf("want online, got %s", n.State)
	}

	// leave -> offline
	rec = h.do("POST", "/api/v1/nodes/"+a.ID+"/leave", nil, bearerHdr(cr.Credential))
	if rec.Code != http.StatusOK {
		t.Fatalf("leave: want 200, got %d", rec.Code)
	}
	if n, _ := h.st.Get(a.ID); n.State != store.StateOffline {
		t.Fatalf("want offline, got %s", n.State)
	}
}

func TestAdoptRequiresAdmin(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt",
		adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAdoptNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/nodes/ghost/adopt",
		adoptReq{PairingCode: "x", Role: store.RoleApp}, adminHdr)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestAdoptWrongPairingCode(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt",
		adoptReq{PairingCode: "NOPE-NOPE", Role: store.RoleApp}, adminHdr)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestAdoptInvalidRole(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt",
		adoptReq{PairingCode: a.PairingCode, Role: store.Role("captain")}, adminHdr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestAdoptTwiceConflicts(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	body := adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}
	if rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", body, adminHdr); rec.Code != 200 {
		t.Fatalf("first adopt: %d", rec.Code)
	}
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", body, adminHdr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

// ---- credential ----

func TestCredentialWrongSecret(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: "wrong"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestCredentialNotYetAdopted(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestCredentialNodeNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/nodes/ghost/credential", credentialReq{EnrollSecret: "x"}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// ---- heartbeat ----

func TestHeartbeatBadCredential(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr("not-the-credential"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHeartbeatPendingNodeRejected(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x") // never adopted -> empty credential
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestHeartbeatNodeNotFound(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/nodes/ghost/heartbeat", nil, bearerHdr("x"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

// ---- list / get / delete ----

func TestListRequiresAdminAndHidesSecrets(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)

	if rec := h.do("GET", "/api/v1/nodes", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth list: want 401, got %d", rec.Code)
	}
	rec := h.do("GET", "/api/v1/nodes", nil, adminHdr)
	if rec.Code != 200 {
		t.Fatalf("list: %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, a.ID) {
		t.Fatalf("list missing node")
	}
	if strings.Contains(body, a.EnrollSecret) || strings.Contains(body, "secret-2") {
		t.Fatalf("list leaked secret/credential: %s", body)
	}
}

func TestGet(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	if rec := h.do("GET", "/api/v1/nodes/"+a.ID, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth get: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/nodes/"+a.ID, nil, adminHdr); rec.Code != 200 {
		t.Fatalf("get: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/nodes/ghost", nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("get missing: %d", rec.Code)
	}
}

func TestDelete(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	if rec := h.do("DELETE", "/api/v1/nodes/"+a.ID, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth delete: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/nodes/"+a.ID, nil, adminHdr); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := h.do("DELETE", "/api/v1/nodes/"+a.ID, nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d", rec.Code)
	}
}

// ---- reaper ----

func TestReapOffline(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "x")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleInference}, adminHdr)
	var cr credentialResp
	mustJSON(t, h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil), &cr)
	h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential))

	// not yet past the timeout
	if n := h.srv.ReapOffline(); n != 0 {
		t.Fatalf("reaped too early: %d", n)
	}
	// advance past the timeout
	h.clock = h.clock.Add(31 * time.Second)
	if n := h.srv.ReapOffline(); n != 1 {
		t.Fatalf("want 1 reaped, got %d", n)
	}
	if node, _ := h.st.Get(a.ID); node.State != store.StateOffline {
		t.Fatalf("want offline, got %s", node.State)
	}
	// a fresh heartbeat brings it back online
	h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential))
	if node, _ := h.st.Get(a.ID); node.State != store.StateOnline {
		t.Fatalf("want online after heartbeat, got %s", node.State)
	}
}

// ---- construction / misc ----

func TestNewValidation(t *testing.T) {
	st := store.NewMemory()
	cases := []Config{
		{AdminKey: "a", JoinToken: "b"},               // no store
		{Store: st, JoinToken: "b"},                   // no admin key
		{Store: st, AdminKey: "a"},                    // no join token
	}
	for i, c := range cases {
		if _, err := New(c); err == nil {
			t.Fatalf("case %d: expected error", i)
		}
	}
	if _, err := New(Config{Store: st, AdminKey: "a", JoinToken: "b"}); err != nil {
		t.Fatalf("valid config errored: %v", err)
	}
}

// TestCORSDisabledByDefault: no configured origin means no CORS headers at
// all — a wildcard would let any website a LAN user visits call the API from
// their browser.
func TestCORSDisabledByDefault(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/api/v1/nodes", nil, adminHdr)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected CORS header %q with no origin configured", got)
	}
}

func TestCORSConfiguredOrigin(t *testing.T) {
	st := store.NewMemory()
	srv, err := New(Config{Store: st, AdminKey: "admin-key", JoinToken: "join-tok", CORSOrigin: "http://localhost:5173"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("OPTIONS", "/api/v1/nodes", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight: want 204, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("CORS origin = %q, want the configured origin (never *)", got)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/api/v1/discovery/announce", nil, joinHdr)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", rec.Code)
	}
}

func TestDefaultGeneratorsProduceUniqueValues(t *testing.T) {
	// exercise the production crypto/rand generators (not the test stubs)
	srv, err := New(Config{Store: store.NewMemory(), AdminKey: "a", JoinToken: "b"})
	if err != nil {
		t.Fatal(err)
	}
	srv2 := srv // same cfg closure
	_ = srv2
	a := srv.cfg.NewID()
	b := srv.cfg.NewID()
	if a == b || !strings.HasPrefix(a, "n-") {
		t.Fatalf("ids not unique/prefixed: %s %s", a, b)
	}
	code := srv.cfg.NewCode()
	if len(code) != 9 || code[4] != '-' {
		t.Fatalf("bad pairing code format: %q", code)
	}
	if srv.cfg.NewSecret() == srv.cfg.NewSecret() {
		t.Fatal("secrets not unique")
	}
}
