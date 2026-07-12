// Package hub is the multi-controller registry: the announce/heartbeat
// pattern one level up (docs/hosted-hub.md, docs/hub-first-setup.md).
// Controllers enroll outbound with an org token and push periodic summaries;
// the hub serves the org's controller/site index. It is a thin index — the
// source of truth stays on each controller.
package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// SiteSummary is one site as reported by a controller.
type SiteSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Machines int    `json:"machines"`
	Online   int    `json:"online"`
}

// Controller is a registry entry, updated by enroll/report.
type Controller struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Version    string        `json:"version,omitempty"`
	Sites      []SiteSummary `json:"sites"`
	EnrolledAt time.Time     `json:"enrolled_at"`
	LastSeen   time.Time     `json:"last_seen"`
	// Online is derived from LastSeen at read time, never stored.
	Online bool `json:"online"`
}

// Store persists registry entries. Implemented by Bolt (hub.db) and Memory.
type Store interface {
	PutController(c *Controller) error
	GetController(id string) (*Controller, error) // ErrNotFound if absent
	ListControllers() ([]*Controller, error)
}

// DiscoveredController is an unclaimed controller visible on the LAN
// (docs/hub-first-setup.md step 3).
type DiscoveredController struct {
	Name string            `json:"name"`
	Addr string            `json:"addr"`
	TXT  map[string]string `json:"txt,omitempty"`
}

// Config wires a hub Server.
type Config struct {
	OrgToken    string // shared secret controllers enroll with (single-org v1)
	Store       Store
	SeenTimeout time.Duration // online -> offline threshold (default 90s)
	Logf        func(string, ...any)
	Now         func() time.Time
	NewID       func() string

	// Discover scans for unclaimed controllers (nil = discovery unsupported;
	// provisioning by direct address still works).
	Discover func(ctx context.Context) ([]DiscoveredController, error)
	// PublicURL is the address controllers should enroll back to. Empty =
	// derive per request from the Host header.
	PublicURL string
	// HTTPClient reaches controllers during provisioning (default 10s timeout).
	HTTPClient *http.Client
}

// Server is the hub API.
type Server struct {
	cfg Config
	mu  sync.Mutex // serializes enroll upserts
}

// New validates cfg and returns a hub server.
func New(cfg Config) *Server {
	if cfg.SeenTimeout <= 0 {
		cfg.SeenTimeout = 90 * time.Second
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewID == nil {
		cfg.NewID = func() string {
			b := make([]byte, 8)
			_, _ = rand.Read(b)
			return "c-" + hex.EncodeToString(b)
		}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Server{cfg: cfg}
}

// Handler returns the hub's HTTP API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// Both paths serve liveness: /healthz for local/self-hosted convention,
	// /api/v1/health because Google's frontend intercepts /healthz on
	// *.run.app and never forwards it to the container.
	health := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /api/v1/health", health)
	mux.HandleFunc("POST /api/v1/enroll", s.handleEnroll)
	mux.HandleFunc("POST /api/v1/controllers/{id}/report", s.handleReport)
	mux.HandleFunc("GET /api/v1/controllers", s.handleList)
	mux.HandleFunc("GET /api/v1/discovery", s.handleDiscovery)
	mux.HandleFunc("POST /api/v1/provision", s.handleProvision)
	return mux
}

// handleDiscovery lists unclaimed controllers currently visible on the LAN —
// the wizard's "Looking for Traego hardware…" step.
func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if !s.orgAuthed(w, r) {
		return
	}
	if s.cfg.Discover == nil {
		writeErr(w, http.StatusNotImplemented, "discovery is not available on this hub")
		return
	}
	found, err := s.cfg.Discover(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "discovery failed: "+err.Error())
		return
	}
	if found == nil {
		found = []DiscoveredController{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"controllers": found})
}

type provisionFwdReq struct {
	Addr      string `json:"addr"` // controller host:port (from discovery or typed)
	ClaimCode string `json:"claim_code"`
	Name      string `json:"name,omitempty"`
}

// handleProvision claims an unclaimed controller on the org's behalf: it
// forwards the claim code plus this hub's URL and org token to the
// controller's one-shot provision endpoint. The controller then enrolls back.
func (s *Server) handleProvision(w http.ResponseWriter, r *http.Request) {
	if !s.orgAuthed(w, r) {
		return
	}
	var req provisionFwdReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Addr == "" || req.ClaimCode == "" {
		writeErr(w, http.StatusBadRequest, "addr and claim_code are required")
		return
	}
	hubURL := s.cfg.PublicURL
	if hubURL == "" {
		hubURL = "http://" + r.Host
	}
	body, _ := json.Marshal(map[string]string{
		"claim_code": req.ClaimCode,
		"hub_url":    hubURL,
		"org_token":  s.cfg.OrgToken,
		"name":       req.Name,
	})
	creq, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"http://"+req.Addr+"/api/v1/provision", bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid controller address")
		return
	}
	creq.Header.Set("Content-Type", "application/json")
	resp, err := s.cfg.HTTPClient.Do(creq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "controller unreachable at "+req.Addr)
		return
	}
	defer resp.Body.Close()
	// relay the controller's verdict (200 claimed / 403 code mismatch / 409 already claimed)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	if resp.StatusCode == http.StatusOK {
		s.cfg.Logf("hub: provisioned controller at %s", req.Addr)
	}
}

type enrollReq struct {
	ID      string `json:"id,omitempty"` // present on re-enroll after restart
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// handleEnroll registers (or re-registers) a controller in the org.
// Idempotent on ID: a controller that restarts keeps its identity.
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.orgAuthed(w, r) {
		return
	}
	var req enrollReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cfg.Now()
	var c *Controller
	if req.ID != "" {
		if existing, err := s.cfg.Store.GetController(req.ID); err == nil {
			c = existing
		}
	}
	if c == nil {
		c = &Controller{ID: s.cfg.NewID(), EnrolledAt: now}
		if req.ID != "" {
			// unknown ID presented (hub reset, or first contact with a
			// controller that minted its own): keep the controller's identity
			c.ID = req.ID
		}
		s.cfg.Logf("hub: controller enrolled: %s (%s)", req.Name, c.ID)
	}
	c.Name, c.Version, c.LastSeen = req.Name, req.Version, now
	if err := s.cfg.Store.PutController(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not persist enrollment")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": c.ID})
}

type reportReq struct {
	Sites []SiteSummary `json:"sites"`
}

// handleReport is the controller heartbeat: refreshes LastSeen and replaces
// the pushed site summary.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	if !s.orgAuthed(w, r) {
		return
	}
	c, err := s.cfg.Store.GetController(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "unknown controller — enroll first")
		return
	}
	var req reportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	c.Sites = req.Sites
	c.LastSeen = s.cfg.Now()
	if err := s.cfg.Store.PutController(c); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not persist report")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleList serves the org's controller index, online state derived from
// LastSeen. This is what the hub UI's site list renders — and it can still
// show an offline site (docs/hosted-hub.md, replicated index).
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	if !s.orgAuthed(w, r) {
		return
	}
	list, err := s.cfg.Store.ListControllers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list controllers")
		return
	}
	now := s.cfg.Now()
	for _, c := range list {
		c.Online = now.Sub(c.LastSeen) < s.cfg.SeenTimeout
	}
	sort.Slice(list, func(i, j int) bool { return list[i].EnrolledAt.Before(list[j].EnrolledAt) })
	writeJSON(w, http.StatusOK, map[string]any{"controllers": list})
}

// orgAuthed checks the org token (Bearer). Single-org v1; accounts and the
// traego.ai identity service replace this per docs/auth-login-design.md.
func (s *Server) orgAuthed(w http.ResponseWriter, r *http.Request) bool {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.OrgToken)) != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid org token")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
