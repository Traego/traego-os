package api

import (
	"net/http"
	"testing"
)

func TestEnableErrorPaths(t *testing.T) {
	h := inferenceHarness(t)
	// requires admin
	if rec := h.do("POST", "/api/v1/models/llama3.2:1b/enable", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("enable without admin: want 401, got %d", rec.Code)
	}
	// unknown model
	if rec := h.do("POST", "/api/v1/models/bogus:1t/enable", nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("enable unknown: want 404, got %d", rec.Code)
	}
}

func TestDisablePaths(t *testing.T) {
	h := inferenceHarness(t)
	// requires admin
	if rec := h.do("POST", "/api/v1/models/llama3.2:1b/disable", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable without admin: want 401, got %d", rec.Code)
	}
	// unknown model
	if rec := h.do("POST", "/api/v1/models/bogus:1t/disable", nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("disable unknown: want 404, got %d", rec.Code)
	}
	// known but not enabled -> idempotent 200
	if rec := h.do("POST", "/api/v1/models/qwen2.5:7b/disable", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("disable not-enabled: want 200, got %d", rec.Code)
	}
}

func TestLeaveRequiresCredential(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "leaver")
	// no credential -> 401
	if rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/leave", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("leave without credential: want 401, got %d", rec.Code)
	}
	// unknown node -> 404
	if rec := h.do("POST", "/api/v1/nodes/ghost/leave", nil, bearerHdr("x")); rec.Code != http.StatusNotFound {
		t.Fatalf("leave ghost node: want 404, got %d", rec.Code)
	}
}
