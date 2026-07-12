// Package api implements the Traego controller's HTTP API: the node
// adoption + presence lifecycle that the join flow depends on.
//
// Lifecycle:
//
//	announce  -> node appears as `pending`, gets an enroll secret + pairing code
//	adopt     -> operator verifies the pairing code, controller issues a credential (`adopted`)
//	credential-> the announcing node trades its enroll secret for the credential
//	heartbeat -> node proves liveness with the credential (`online`)
//	leave/reap-> graceful departure or missed heartbeats (`offline`)
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/inference"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/store"
)

// Config configures a Server. Store, AdminKey and JoinToken are required.
type Config struct {
	Store            store.Store
	AdminKey         string        // bearer token for operator/admin endpoints
	JoinToken        string        // shared token a node presents to announce
	HeartbeatTimeout time.Duration // online -> offline after this without a heartbeat

	// CORSOrigin, when set, is the single origin allowed to call the API
	// cross-origin (e.g. the Vite dev server). Empty means no CORS headers:
	// the embedded UI is same-origin and needs none.
	CORSOrigin string

	// Logf, when set, receives security-relevant events (auth failures).
	Logf func(format string, args ...any)

	// CA, when set, enables certificate issuance and the mTLS data plane
	// (SecureHandler). CertTTL bounds issued node-cert lifetime.
	CA      *ca.CA
	CertTTL time.Duration

	// System, when set, exposes the controller's own host metrics at
	// GET /api/v1/system.
	System func() metrics.Sample

	// Inference, when set, enables the model catalog + chat endpoints.
	Inference *inference.Manager

	// HomeSite is the site the controller itself lives in. Defaults to
	// {local, "Local site"} and is ensured to exist on startup.
	HomeSite store.Site

	// Test injection points. Left nil, secure defaults are used.
	NewID     func() string
	NewSecret func() string
	NewCode   func() string
	Now       func() time.Time
}

// Server is the controller API. Construct with New.
type Server struct {
	cfg Config

	actMu         sync.Mutex
	actCur        ActivitySample
	actHist       []float64
	actPrevTokens int64
	actPrevTime   time.Time

	// per-IP auth-failure limiter (guessing resistance for all secrets)
	authMu    sync.Mutex
	authFails map[string]*failWindow
}

// New validates cfg and returns a Server with defaults applied.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("api: Store is required")
	}
	if cfg.AdminKey == "" {
		return nil, errors.New("api: AdminKey is required")
	}
	if cfg.JoinToken == "" {
		return nil, errors.New("api: JoinToken is required")
	}
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 30 * time.Second
	}
	if cfg.CertTTL <= 0 {
		cfg.CertTTL = 24 * time.Hour
	}
	if cfg.NewID == nil {
		cfg.NewID = func() string { return "n-" + randHex(8) }
	}
	if cfg.NewSecret == nil {
		cfg.NewSecret = func() string { return randHex(24) }
	}
	if cfg.NewCode == nil {
		cfg.NewCode = randPairingCode
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HomeSite.ID == "" {
		cfg.HomeSite = store.Site{ID: "local", Name: "Local site"}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	_ = cfg.Store.PutSite(&cfg.HomeSite) // ensure the home site exists
	return &Server{cfg: cfg, authFails: map[string]*failWindow{}}, nil
}

// Handler returns the routed HTTP handler (method-aware via Go 1.22 patterns,
// wrapped in permissive CORS so the web app can call it from another origin).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/v1/discovery/announce", s.handleAnnounce)
	mux.HandleFunc("POST /api/v1/nodes/{id}/credential", s.handleCredential)
	mux.HandleFunc("POST /api/v1/nodes/{id}/heartbeat", s.handleHeartbeat)
	mux.HandleFunc("POST /api/v1/nodes/{id}/leave", s.handleLeave)
	mux.HandleFunc("POST /api/v1/nodes/{id}/adopt", s.handleAdopt)
	mux.HandleFunc("GET /api/v1/sites", s.handleSites)
	mux.HandleFunc("POST /api/v1/sites", s.handleCreateSite)
	mux.HandleFunc("GET /api/v1/nodes", s.handleList)
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.handleGet)
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.handleDelete)
	mux.HandleFunc("GET /api/v1/activity", s.handleActivity)
	if s.cfg.CA != nil {
		mux.HandleFunc("GET /api/v1/ca", s.handleCA)
		mux.HandleFunc("POST /api/v1/nodes/{id}/certificate", s.handleCertificate)
	}
	if s.cfg.System != nil {
		mux.HandleFunc("GET /api/v1/system", s.handleSystem)
	}
	if s.cfg.Inference != nil {
		mux.HandleFunc("GET /api/v1/modules", s.handleModules)
		mux.HandleFunc("POST /api/v1/modules/ai/install", s.handleInstallAI)
		mux.HandleFunc("POST /api/v1/modules/ai/uninstall", s.handleUninstallAI)
		mux.HandleFunc("GET /api/v1/models", s.handleModels)
		mux.HandleFunc("POST /api/v1/models/{id}/deploy", s.handleDeploy)
		mux.HandleFunc("POST /api/v1/models/{id}/enable", s.handleEnable)
		mux.HandleFunc("POST /api/v1/models/{id}/disable", s.handleDisable)
		mux.HandleFunc("GET /api/v1/chat/models", s.handleChatModels)
		mux.HandleFunc("POST /api/v1/chat", s.handleChat)
	}
	return cors(mux, s.cfg.CORSOrigin)
}

// SecureHandler is the mTLS data plane, served on a separate TLS listener whose
// tls.Config requires a CA-signed client certificate. Requests here are already
// cryptographically authenticated; the handler reads the node identity straight
// from the verified client certificate's CommonName.
func (s *Server) SecureHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/secure/whoami", s.handleWhoami)
	mux.HandleFunc("POST /api/v1/nodes/{id}/heartbeat", s.handleSecureHeartbeat)
	return mux
}

// ---- Handlers ----

type announceReq struct {
	Name  string      `json:"name"`
	Site  string      `json:"site"` // declared site; defaults to the controller's home site
	Class store.Class `json:"class"`
	Specs store.Specs `json:"specs"`
}

type announceResp struct {
	ID           string      `json:"id"`
	EnrollSecret string      `json:"enroll_secret"`
	PairingCode  string      `json:"pairing_code"`
	State        store.State `json:"state"`
}

func (s *Server) handleAnnounce(w http.ResponseWriter, r *http.Request) {
	if s.authLimited(w, r) {
		return
	}
	if !ctEq(r.Header.Get("X-Traego-Join-Token"), s.cfg.JoinToken) {
		s.authFailure(r, "join token")
		writeErr(w, http.StatusUnauthorized, "invalid join token")
		return
	}
	var req announceReq
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Class == "" {
		req.Class = store.ClassPersistent
	}
	if !req.Class.Valid() {
		writeErr(w, http.StatusBadRequest, "invalid class")
		return
	}
	site := req.Site
	if site == "" {
		site = s.cfg.HomeSite.ID
	}
	// auto-register a site a node declares but the controller hasn't seen yet
	if _, err := s.cfg.Store.GetSite(site); err != nil {
		_ = s.cfg.Store.PutSite(&store.Site{ID: site, Name: site})
	}
	n := &store.Node{
		ID:           s.cfg.NewID(),
		Name:         req.Name,
		Site:         site,
		Class:        req.Class,
		State:        store.StatePending,
		Specs:        req.Specs,
		PairingCode:  s.cfg.NewCode(),
		EnrollSecret: s.cfg.NewSecret(),
		CreatedAt:    s.cfg.Now(),
	}
	if err := s.cfg.Store.Create(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not register node")
		return
	}
	writeJSON(w, http.StatusCreated, announceResp{
		ID: n.ID, EnrollSecret: n.EnrollSecret, PairingCode: n.PairingCode, State: n.State,
	})
}

type adoptReq struct {
	PairingCode string     `json:"pairing_code"`
	Role        store.Role `json:"role"`
}

func (s *Server) handleAdopt(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	n, ok := s.lookup(w, r.PathValue("id"))
	if !ok {
		return
	}
	var req adoptReq
	if !decode(w, r, &req) {
		return
	}
	if n.State != store.StatePending {
		writeErr(w, http.StatusConflict, "node is not pending adoption")
		return
	}
	if !ctEq(req.PairingCode, n.PairingCode) {
		s.authFailure(r, "pairing code")
		writeErr(w, http.StatusForbidden, "pairing code mismatch")
		return
	}
	if !store.ValidRole(req.Role) {
		writeErr(w, http.StatusBadRequest, "invalid role")
		return
	}
	n.Role = req.Role
	n.State = store.StateAdopted
	n.Credential = s.cfg.NewSecret()
	if err := s.cfg.Store.Update(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not adopt node")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type credentialReq struct {
	EnrollSecret string `json:"enroll_secret"`
}

type credentialResp struct {
	Credential string      `json:"credential"`
	Role       store.Role  `json:"role"`
	State      store.State `json:"state"`
}

func (s *Server) handleCredential(w http.ResponseWriter, r *http.Request) {
	n, ok := s.lookup(w, r.PathValue("id"))
	if !ok {
		return
	}
	if s.authLimited(w, r) {
		return
	}
	var req credentialReq
	if !decode(w, r, &req) {
		return
	}
	if !ctEq(req.EnrollSecret, n.EnrollSecret) {
		s.authFailure(r, "enroll secret")
		writeErr(w, http.StatusForbidden, "invalid enroll secret")
		return
	}
	if n.State == store.StatePending {
		writeErr(w, http.StatusConflict, "node not yet adopted")
		return
	}
	// One-shot: the enroll secret is spent the moment the credential is handed
	// out, so it can't serve as a second permanent credential. A node that
	// loses the response must re-announce.
	n.EnrollSecret = ""
	if err := s.cfg.Store.Update(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not issue credential")
		return
	}
	writeJSON(w, http.StatusOK, credentialResp{Credential: n.Credential, Role: n.Role, State: n.State})
}

type heartbeatResp struct {
	Role  store.Role  `json:"role"`
	State store.State `json:"state"`
}

type heartbeatReq struct {
	Metrics *metrics.Sample `json:"metrics"`
}

// readMetrics decodes an optional metrics body; an empty/invalid body yields nil
// so heartbeats without metrics still work.
func readMetrics(r *http.Request) *metrics.Sample {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil || len(body) == 0 {
		return nil
	}
	var hb heartbeatReq
	if json.Unmarshal(body, &hb) != nil {
		return nil
	}
	return hb.Metrics
}

// handleSystem reports the controller's own host metrics.
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.cfg.System())
}

// machineMemGB is the total RAM the controller's host reports (for model fit).
func (s *Server) machineMemGB() float64 {
	if s.cfg.System != nil {
		if sys := s.cfg.System(); sys.MemTotalGB > 0 {
			return sys.MemTotalGB
		}
	}
	return 1e6 // unknown -> don't gate fit
}

// moduleView is an installed (or installable) module on the controller node.
type moduleView struct {
	Type      string  `json:"type"`
	Installed bool    `json:"installed"`
	VRAMGB    float64 `json:"vram_gb,omitempty"`
}

// handleModules lists the controller node's modules. The controller module is
// always present; the AI module reports whether it's been installed + its VRAM
// reservation.
func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	mods := []moduleView{
		{Type: "controller", Installed: true},
		{Type: "ai", Installed: s.cfg.Inference.Installed(), VRAMGB: s.cfg.Inference.VRAMReservedGB()},
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": mods})
}

func (s *Server) handleInstallAI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		VRAMGB float64 `json:"vram_gb"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.VRAMGB < 0 {
		writeErr(w, http.StatusBadRequest, "vram_gb must be >= 0")
		return
	}
	if err := s.cfg.Inference.Install(r.Context(), req.VRAMGB); err != nil {
		msg := "AI backend unreachable — is Ollama running?"
		if !errors.Is(err, inference.ErrBackendDown) {
			msg = err.Error() // supervisor errors are actionable (e.g. binary missing)
		}
		writeErr(w, http.StatusBadGateway, msg)
		return
	}
	writeJSON(w, http.StatusOK, moduleView{Type: "ai", Installed: true, VRAMGB: req.VRAMGB})
}

func (s *Server) handleUninstallAI(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.cfg.Inference.Uninstall()
	writeJSON(w, http.StatusOK, map[string]string{"uninstalled": "ai"})
}

// requireAIModule gates the AI endpoints behind an explicit install. Returns
// false (and writes 409) when the module isn't installed.
func (s *Server) requireAIModule(w http.ResponseWriter) bool {
	if !s.cfg.Inference.Installed() {
		writeErr(w, http.StatusConflict, "AI module is not installed")
		return false
	}
	return true
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	mem := s.machineMemGB()
	writeJSON(w, http.StatusOK, map[string]any{
		"models":          s.cfg.Inference.Models(r.Context(), mem),
		"enabled":         s.cfg.Inference.Enabled(),
		"backend_healthy": s.cfg.Inference.Healthy(r.Context()),
		"machine_mem_gb":  mem,
	})
}

func (s *Server) handleDeploy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	if err := s.cfg.Inference.Deploy(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, "unknown model")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "deploying"})
}

func (s *Server) handleEnable(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	id := r.PathValue("id")
	switch err := s.cfg.Inference.Enable(r.Context(), id); {
	case errors.Is(err, inference.ErrUnknownModel):
		writeErr(w, http.StatusNotFound, "unknown model")
	case errors.Is(err, inference.ErrNotDeployed):
		writeErr(w, http.StatusConflict, "model is not deployed yet")
	case err != nil:
		writeErr(w, http.StatusBadGateway, "inference backend error")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"enabled": id})
	}
}

func (s *Server) handleDisable(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	id := r.PathValue("id")
	if err := s.cfg.Inference.Disable(id); errors.Is(err, inference.ErrUnknownModel) {
		writeErr(w, http.StatusNotFound, "unknown model")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"disabled": id})
}

// handleChat is the end-user inference endpoint. It accepts the full
// conversation (messages) so the model has context; a bare prompt is also
// accepted as a single-turn shorthand. Admin-authed for now: an unauthenticated
// inference endpoint lets any reachable client (including any website in a
// LAN user's browser) burn the GPU. A dedicated chat token is a later slice.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	var req struct {
		Messages []inference.Message `json:"messages"`
		Prompt   string              `json:"prompt"`
		Model    string              `json:"model"` // optional: pick a model; "" = enabled default
	}
	if !decode(w, r, &req) {
		return
	}
	msgs := req.Messages
	if len(msgs) == 0 && strings.TrimSpace(req.Prompt) != "" {
		msgs = []inference.Message{{Role: "user", Content: req.Prompt}}
	}
	if len(msgs) == 0 {
		writeErr(w, http.StatusBadRequest, "messages or prompt is required")
		return
	}
	switch reply, err := s.cfg.Inference.Chat(r.Context(), req.Model, msgs); {
	case errors.Is(err, inference.ErrNoModelEnabled):
		writeErr(w, http.StatusConflict, "no model is enabled yet")
	case errors.Is(err, inference.ErrUnknownModel):
		writeErr(w, http.StatusBadRequest, "unknown model")
	case errors.Is(err, inference.ErrNotDeployed):
		writeErr(w, http.StatusConflict, "selected model is not deployed")
	case err != nil:
		writeErr(w, http.StatusBadGateway, "inference failed")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"reply": reply})
	}
}

// handleChatModels lists the models a chat user can select — the set enabled
// for end users.
func (s *Server) handleChatModels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if !s.requireAIModule(w) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models":  s.cfg.Inference.EnabledList(),
		"default": s.cfg.Inference.Enabled(),
	})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	n, ok := s.nodeByCredential(w, r)
	if !ok {
		return
	}
	if m := readMetrics(r); m != nil {
		n.Metrics = m
	}
	n.State = store.StateOnline
	// A bearer heartbeat means the node is NOT currently on the mTLS plane —
	// reflect that honestly so the UI never shows a stale "secured" badge.
	n.Secured = false
	n.LastHeartbeat = s.cfg.Now()
	if err := s.cfg.Store.Update(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not record heartbeat")
		return
	}
	writeJSON(w, http.StatusOK, heartbeatResp{Role: n.Role, State: n.State})
}

func (s *Server) handleLeave(w http.ResponseWriter, r *http.Request) {
	n, ok := s.nodeByCredential(w, r)
	if !ok {
		return
	}
	n.State = store.StateOffline
	if err := s.cfg.Store.Update(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not record leave")
		return
	}
	writeJSON(w, http.StatusOK, map[string]store.State{"state": n.State})
}

type siteView struct {
	store.Site
	IsHome    bool `json:"is_home"`
	NodeCount int  `json:"node_count"`
}

func (s *Server) handleSites(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	sites, err := s.cfg.Store.ListSites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list sites")
		return
	}
	counts := map[string]int{}
	if nodes, err := s.cfg.Store.List(); err == nil {
		for _, n := range nodes {
			counts[n.Site]++
		}
	}
	out := make([]siteView, 0, len(sites))
	for _, st := range sites {
		out = append(out, siteView{Site: *st, IsHome: st.ID == s.cfg.HomeSite.ID, NodeCount: counts[st.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sites": out, "home": s.cfg.HomeSite.ID})
}

func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	site := store.Site{ID: "site-" + randHex(4), Name: req.Name}
	if err := s.cfg.Store.PutSite(&site); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create site")
		return
	}
	writeJSON(w, http.StatusCreated, site)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	nodes, err := s.cfg.Store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list nodes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	n, ok := s.lookup(w, r.PathValue("id"))
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if err := s.cfg.Store.Delete(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "node not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not delete node")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCA returns the CA certificate (public; lets nodes trust the controller
// and verify peer certs).
func (s *Server) handleCA(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write(s.cfg.CA.CertPEM())
}

type certResp struct {
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
}

// handleCertificate signs a node's CSR into a client certificate. The node
// authenticates with its bearer credential; the issued cert's CommonName is
// forced to the node's own id, so it cannot mint another node's identity.
func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	n, ok := s.nodeByCredential(w, r)
	if !ok {
		return
	}
	csr, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "could not read CSR")
		return
	}
	certPEM, err := s.cfg.CA.SignCSR(csr, n.ID, s.cfg.CertTTL)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid certificate request")
		return
	}
	writeJSON(w, http.StatusOK, certResp{Certificate: string(certPEM), CA: string(s.cfg.CA.CertPEM())})
}

// handleWhoami echoes the verified mTLS client identity.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	cn := peerCN(r)
	if cn == "" {
		writeErr(w, http.StatusUnauthorized, "no client certificate")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"node_id": cn, "secured": true})
}

// handleSecureHeartbeat is the mTLS heartbeat: the client cert proves identity,
// so there is no bearer token. The cert CN must match the {id} in the path.
func (s *Server) handleSecureHeartbeat(w http.ResponseWriter, r *http.Request) {
	cn := peerCN(r)
	if cn == "" {
		writeErr(w, http.StatusUnauthorized, "no client certificate")
		return
	}
	if cn != r.PathValue("id") {
		writeErr(w, http.StatusForbidden, "certificate identity does not match node")
		return
	}
	n, ok := s.lookup(w, cn)
	if !ok {
		return
	}
	if m := readMetrics(r); m != nil {
		n.Metrics = m
	}
	n.State = store.StateOnline
	n.Secured = true
	n.LastHeartbeat = s.cfg.Now()
	if err := s.cfg.Store.Update(n); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not record heartbeat")
		return
	}
	writeJSON(w, http.StatusOK, heartbeatResp{Role: n.Role, State: n.State})
}

func peerCN(r *http.Request) string {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return ""
	}
	return r.TLS.PeerCertificates[0].Subject.CommonName
}

// ---- Activity (pool inference workload) ----
//
// Until a real inference runtime reports throughput, the controller synthesizes
// a live workload signal driven by how many inference nodes are online. The
// signal still flows through the real API + history buffer, so the dashboard
// chart is genuinely live.

const activityCap = 60 // ~2 minutes at one sample / 2s

// ActivitySample is the current synthesized workload.
type ActivitySample struct {
	TokensPerSec float64 `json:"tokens_per_sec"`
	ActiveJobs   int     `json:"active_jobs"`
}

type activityResp struct {
	TokensPerSec float64   `json:"tokens_per_sec"`
	ActiveJobs   int       `json:"active_jobs"`
	History      []float64 `json:"history"`
}

// stepActivity advances the workload toward a target set by the number of online
// inference nodes (~90 tok/s each), with a noise term for liveliness. Pure and
// deterministic for testing.
func stepActivity(cur ActivitySample, onlineInference int, noise float64) ActivitySample {
	target := float64(onlineInference) * 90
	tps := cur.TokensPerSec + (target-cur.TokensPerSec)*0.3 + noise
	if tps < 0 {
		tps = 0
	}
	return ActivitySample{TokensPerSec: round1tps(tps), ActiveJobs: onlineInference * 3}
}

// RecordActivity records one workload sample. Call on an interval. With a local
// inference backend the tokens/sec is REAL (cumulative tokens differentiated
// over wall time); otherwise it falls back to a signal synthesized from the
// number of online inference nodes.
func (s *Server) RecordActivity() {
	s.actMu.Lock()
	defer s.actMu.Unlock()

	if s.cfg.Inference != nil {
		now := s.cfg.Now()
		tokens := s.cfg.Inference.TokensGenerated()
		var tps float64
		if !s.actPrevTime.IsZero() {
			if dt := now.Sub(s.actPrevTime).Seconds(); dt > 0 {
				tps = float64(tokens-s.actPrevTokens) / dt
			}
		}
		s.actPrevTokens, s.actPrevTime = tokens, now
		s.actCur = ActivitySample{TokensPerSec: round1tps(tps), ActiveJobs: s.cfg.Inference.Inflight()}
	} else {
		online := 0
		if nodes, err := s.cfg.Store.List(); err == nil {
			for _, n := range nodes {
				if n.State == store.StateOnline && n.Role == store.RoleInference {
					online++
				}
			}
		}
		s.actCur = stepActivity(s.actCur, online, (mrand.Float64()-0.5)*40)
	}

	s.actHist = append(s.actHist, s.actCur.TokensPerSec)
	if len(s.actHist) > activityCap {
		s.actHist = s.actHist[len(s.actHist)-activityCap:]
	}
}

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	s.actMu.Lock()
	hist := make([]float64, len(s.actHist))
	copy(hist, s.actHist)
	cur := s.actCur
	s.actMu.Unlock()
	writeJSON(w, http.StatusOK, activityResp{TokensPerSec: cur.TokensPerSec, ActiveJobs: cur.ActiveJobs, History: hist})
}

func round1tps(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

// ReapOffline marks online nodes that have missed their heartbeat window as
// offline. It returns the number of nodes transitioned. Safe to call from a
// ticker; exposed for deterministic testing with an injected clock.
func (s *Server) ReapOffline() int {
	nodes, err := s.cfg.Store.List()
	if err != nil {
		return 0
	}
	now := s.cfg.Now()
	var n int
	for _, snap := range nodes {
		// Cheap pre-filter on the snapshot...
		if snap.State != store.StateOnline || now.Sub(snap.LastHeartbeat) <= s.cfg.HeartbeatTimeout {
			continue
		}
		// ...then re-check under the write lock so a heartbeat that landed
		// after the snapshot isn't clobbered back to offline.
		_ = s.cfg.Store.Mutate(snap.ID, func(nd *store.Node) bool {
			if nd.State == store.StateOnline && now.Sub(nd.LastHeartbeat) > s.cfg.HeartbeatTimeout {
				nd.State = store.StateOffline
				n++
				return true
			}
			return false
		})
	}
	return n
}

// ---- Auth-failure rate limiting ----
//
// Every secret comparison in the API records failures per source IP; an IP
// that keeps failing gets cut off before any comparison runs. This turns
// online guessing of the admin key / join token / credentials from unlimited
// tries into a few per minute, and makes attempts visible in the log.

const (
	authFailLimit  = 10          // failures allowed per window per IP
	authFailWindow = time.Minute // fixed window
)

type failWindow struct {
	count int
	start time.Time
}

// authLimited reports (and responds) whether the caller has exhausted its
// failure budget. Call before comparing any secret.
func (s *Server) authLimited(w http.ResponseWriter, r *http.Request) bool {
	ip := clientIP(r)
	now := s.cfg.Now()
	s.authMu.Lock()
	fw := s.authFails[ip]
	limited := fw != nil && now.Sub(fw.start) < authFailWindow && fw.count >= authFailLimit
	s.authMu.Unlock()
	if limited {
		writeErr(w, http.StatusTooManyRequests, "too many failed authentication attempts, retry later")
	}
	return limited
}

// authFailure records a failed secret check and logs it with the source IP.
func (s *Server) authFailure(r *http.Request, what string) {
	ip := clientIP(r)
	now := s.cfg.Now()
	s.authMu.Lock()
	fw := s.authFails[ip]
	if fw == nil || now.Sub(fw.start) >= authFailWindow {
		// new window; also an opportunistic prune of stale entries
		for k, v := range s.authFails {
			if now.Sub(v.start) >= authFailWindow {
				delete(s.authFails, k)
			}
		}
		fw = &failWindow{start: now}
		s.authFails[ip] = fw
	}
	fw.count++
	n := fw.count
	s.authMu.Unlock()
	s.cfg.Logf("auth failure (%s) from %s (%d/%d in window)", what, ip, n, authFailLimit)
}

func clientIP(r *http.Request) string {
	// Deliberately not honoring X-Forwarded-For: the controller serves
	// clients directly, and a spoofable header would defeat the limiter.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- Helpers ----

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.authLimited(w, r) {
		return false
	}
	if !ctEq(bearer(r), s.cfg.AdminKey) {
		s.authFailure(r, "admin key")
		writeErr(w, http.StatusUnauthorized, "admin authorization required")
		return false
	}
	return true
}

func (s *Server) lookup(w http.ResponseWriter, id string) (*store.Node, bool) {
	n, err := s.cfg.Store.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return nil, false
	}
	return n, true
}

// nodeByCredential authenticates a node-scoped request: the {id} must exist and
// the bearer token must match that node's issued credential.
func (s *Server) nodeByCredential(w http.ResponseWriter, r *http.Request) (*store.Node, bool) {
	if s.authLimited(w, r) {
		return nil, false
	}
	n, err := s.cfg.Store.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "node not found")
		return nil, false
	}
	// An empty credential (never adopted) must never authenticate.
	if n.Credential == "" || !ctEq(bearer(r), n.Credential) {
		s.authFailure(r, "node credential")
		writeErr(w, http.StatusUnauthorized, "invalid node credential")
		return nil, false
	}
	return n, true
}

func bearer(r *http.Request) string {
	const p = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(p) && h[:len(p)] == p {
		return h[len(p):]
	}
	return ""
}

// ctEq is a constant-time string compare; both empty returns false so missing
// secrets never match.
func ctEq(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// cors allows exactly one configured origin (for the Vite dev server). With no
// origin configured it adds no CORS headers at all: the embedded UI is
// same-origin, and a wildcard here would let any website a LAN user visits
// call the API from their browser.
func cors(next http.Handler, origin string) http.Handler {
	if origin == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Traego-Join-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func randHex(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// randPairingCode returns a human-confirmable code like "4F2A-9C1D" using an
// unambiguous alphabet (no 0/O, 1/I).
func randPairingCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, alphabet[int(v)%len(alphabet)])
	}
	return string(out)
}
