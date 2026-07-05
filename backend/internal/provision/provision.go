// Package provision is the controller side of hub-first setup
// (docs/hub-first-setup.md): while a controller is unclaimed, a hub (or the
// get.traego.io installer) may claim it over one authenticated call, gated by
// the claim code printed on the controller's console. Claiming writes the hub
// configuration; it never works twice — a claimed controller returns 409
// until its claim is explicitly reset.
package provision

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Claim is what provisioning grants: membership in an org and the hub to
// enroll with. Persisted by the controller (store meta) and used at boot.
type Claim struct {
	HubURL    string    `json:"hub_url"`
	OrgToken  string    `json:"org_token"`
	Name      string    `json:"name,omitempty"` // display name the wizard chose
	ClaimedAt time.Time `json:"claimed_at"`
}

// Config wires a Handler.
type Config struct {
	ClaimCode string             // printed on the controller console at boot
	Claimed   func() bool        // true once an owner or claim exists
	Apply     func(Claim) error  // persist + activate (start hublink, stop beacon)
	Logf      func(string, ...any)
	Now       func() time.Time
}

// Handler answers POST /api/v1/provision.
type Handler struct {
	cfg Config
	mu  sync.Mutex // one claim attempt at a time; first success wins
}

// New builds a provisioning handler.
func New(cfg Config) *Handler {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Handler{cfg: cfg}
}

// NewClaimCode returns a human-readable code like "7GK4-PW2N" (the same
// alphabet as node pairing codes — one gesture language everywhere).
func NewClaimCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	out := make([]byte, 9)
	for i, c := range b {
		out[i+i/4] = alphabet[int(c)%len(alphabet)]
	}
	out[4] = '-'
	return string(out)
}

type provisionReq struct {
	ClaimCode string `json:"claim_code"`
	HubURL    string `json:"hub_url"`
	OrgToken  string `json:"org_token"`
	Name      string `json:"name,omitempty"`
}

// ServeHTTP implements the one-shot claim.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg.Claimed() {
		writeErr(w, http.StatusConflict, "controller is already claimed")
		return
	}
	var req provisionReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.ClaimCode), []byte(h.cfg.ClaimCode)) != 1 {
		h.cfg.Logf("provision: claim code mismatch from %s", r.RemoteAddr)
		time.Sleep(time.Second) // cheap guessing brake on top of the API limiter
		writeErr(w, http.StatusForbidden, "claim code mismatch")
		return
	}
	if req.HubURL == "" || req.OrgToken == "" {
		writeErr(w, http.StatusBadRequest, "hub_url and org_token are required")
		return
	}
	claim := Claim{HubURL: req.HubURL, OrgToken: req.OrgToken, Name: req.Name, ClaimedAt: h.cfg.Now()}
	if err := h.cfg.Apply(claim); err != nil {
		writeErr(w, http.StatusInternalServerError, "could not apply claim")
		return
	}
	h.cfg.Logf("provision: claimed by hub %s", req.HubURL)
	writeJSON(w, http.StatusOK, map[string]string{"status": "claimed", "hub_url": req.HubURL})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
