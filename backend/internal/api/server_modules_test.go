package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/traego/traego/internal/inference"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/ollama"
	"github.com/traego/traego/internal/store"
)

// uninstalledHarness has an Inference manager that has NOT been installed.
func uninstalledHarness(t *testing.T) *harness {
	t.Helper()
	ol := mockOllama(t)
	mgr := inference.NewManager(ollama.New(ol.URL))
	st := store.NewMemory()
	h := &harness{st: st, clock: time.Unix(1_700_000_000, 0)}
	srv, err := New(Config{
		Store: st, AdminKey: "admin-key", JoinToken: "join-tok",
		System:    func() metrics.Sample { return metrics.Sample{MemTotalGB: 24} },
		Inference: mgr,
		Now:       func() time.Time { return h.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv, h.h = srv, srv.Handler()
	return h
}

func TestModulesListsAIInstallState(t *testing.T) {
	h := uninstalledHarness(t)
	var out struct {
		Modules []struct {
			Type      string  `json:"type"`
			Installed bool    `json:"installed"`
			VRAMGB    float64 `json:"vram_gb"`
		} `json:"modules"`
	}
	mustJSON(t, h.do("GET", "/api/v1/modules", nil, adminHdr), &out)
	var ai, ctl bool
	for _, m := range out.Modules {
		if m.Type == "controller" && m.Installed {
			ctl = true
		}
		if m.Type == "ai" && !m.Installed {
			ai = true
		}
	}
	if !ctl || !ai {
		t.Fatalf("want controller installed + ai not installed: %+v", out.Modules)
	}
}

func TestAIEndpointsGatedUntilInstalled(t *testing.T) {
	h := uninstalledHarness(t)
	// every AI endpoint should 409 until the module is installed
	for _, ep := range []struct {
		method, path string
		hdr          map[string]string
	}{
		{"GET", "/api/v1/models", adminHdr},
		{"POST", "/api/v1/models/llama3.2:1b/deploy", adminHdr},
		{"POST", "/api/v1/models/llama3.2:1b/enable", adminHdr},
		{"GET", "/api/v1/chat/models", adminHdr},
		{"POST", "/api/v1/chat", adminHdr},
	} {
		if rec := h.do(ep.method, ep.path, map[string]string{"prompt": "hi"}, ep.hdr); rec.Code != http.StatusConflict {
			t.Fatalf("%s %s before install: want 409, got %d", ep.method, ep.path, rec.Code)
		}
	}
}

func TestInstallAndUninstallAIModule(t *testing.T) {
	h := uninstalledHarness(t)

	// install requires admin
	if rec := h.do("POST", "/api/v1/modules/ai/install", map[string]float64{"vram_gb": 16}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("install without admin: want 401, got %d", rec.Code)
	}
	// install (mock Ollama is healthy)
	rec := h.do("POST", "/api/v1/modules/ai/install", map[string]float64{"vram_gb": 16}, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("install: %d", rec.Code)
	}
	// now an AI endpoint works
	if rec := h.do("GET", "/api/v1/models", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("models after install: want 200, got %d", rec.Code)
	}
	// modules reflects the reservation
	if !h.srv.cfg.Inference.Installed() || h.srv.cfg.Inference.VRAMReservedGB() != 16 {
		t.Fatalf("install state wrong: installed=%v vram=%v", h.srv.cfg.Inference.Installed(), h.srv.cfg.Inference.VRAMReservedGB())
	}
	// uninstall -> gated again
	if rec := h.do("POST", "/api/v1/modules/ai/uninstall", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("uninstall: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/models", nil, adminHdr); rec.Code != http.StatusConflict {
		t.Fatalf("models after uninstall: want 409, got %d", rec.Code)
	}
}

func TestInstallAIBackendDown(t *testing.T) {
	// a manager pointed at a dead backend can't install
	mgr := inference.NewManager(ollama.New("http://127.0.0.1:1"))
	st := store.NewMemory()
	srv, err := New(Config{Store: st, AdminKey: "admin-key", JoinToken: "join-tok", Inference: mgr})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{st: st, srv: srv, h: srv.Handler()}
	if rec := h.do("POST", "/api/v1/modules/ai/install", map[string]float64{"vram_gb": 8}, adminHdr); rec.Code != http.StatusBadGateway {
		t.Fatalf("install with backend down: want 502, got %d", rec.Code)
	}
}
