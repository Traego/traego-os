package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/traego/traego/internal/inference"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/ollama"
	"github.com/traego/traego/internal/store"
)

// mockOllama is a stateful fake: pulling a model adds it to /api/tags.
func mockOllama(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	pulled := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		type nm struct {
			Name string `json:"name"`
		}
		out := struct {
			Models []nm `json:"models"`
		}{}
		for k := range pulled {
			out.Models = append(out.Models, nm{k})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /api/pull", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"status":"downloading","completed":1,"total":2}`+"\n")
		io.WriteString(w, `{"status":"success","completed":2,"total":2}`+"\n")
		mu.Lock()
		pulled[body.Name] = true
		mu.Unlock()
	})
	mux.HandleFunc("POST /api/chat", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"message":{"role":"assistant","content":"42"},"eval_count":12,"eval_duration":300000000}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func inferenceHarness(t *testing.T) *harness {
	t.Helper()
	ol := mockOllama(t)
	mgr := inference.NewManager(ollama.New(ol.URL))
	if err := mgr.Install(context.Background(), 16); err != nil {
		t.Fatalf("install AI module: %v", err)
	}
	st := store.NewMemory()
	h := &harness{st: st, clock: time.Unix(1_700_000_000, 0)}
	srv, err := New(Config{
		Store: st, AdminKey: "admin-key", JoinToken: "join-tok",
		System:    func() metrics.Sample { return metrics.Sample{MemTotalGB: 24, MemUsedGB: 2} },
		Inference: mgr,
		Now:       func() time.Time { return h.clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	h.h = srv.Handler()
	return h
}

func TestInferenceFullFlow(t *testing.T) {
	h := inferenceHarness(t)

	// list models (admin)
	rec := h.do("GET", "/api/v1/models", nil, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("models: %d", rec.Code)
	}
	var lst struct {
		Models []struct {
			ID       string `json:"id"`
			Fits     bool   `json:"fits"`
			Deployed bool   `json:"deployed"`
			MinMemGB float64 `json:"min_mem_gb"`
		} `json:"models"`
		Enabled      string  `json:"enabled"`
		MachineMemGB float64 `json:"machine_mem_gb"`
	}
	mustJSON(t, rec, &lst)
	if lst.MachineMemGB != 24 || len(lst.Models) == 0 || lst.Enabled != "" {
		t.Fatalf("bad models resp: %+v", lst)
	}
	for _, m := range lst.Models { // fit reflects 24GB machine
		if m.Fits != (m.MinMemGB <= 24) {
			t.Fatalf("%s fit wrong", m.ID)
		}
	}

	// chat before any model is enabled -> 409
	if rec := h.do("POST", "/api/v1/chat", map[string]string{"prompt": "hi"}, adminHdr); rec.Code != http.StatusConflict {
		t.Fatalf("chat-before-enable: want 409, got %d", rec.Code)
	}

	// deploy
	if rec := h.do("POST", "/api/v1/models/llama3.2:1b/deploy", nil, adminHdr); rec.Code != http.StatusAccepted {
		t.Fatalf("deploy: %d", rec.Code)
	}
	deployed := waitForAPI(time.Second, func() bool {
		r := h.do("GET", "/api/v1/models", nil, adminHdr)
		var l struct {
			Models []struct {
				ID       string `json:"id"`
				Deployed bool   `json:"deployed"`
			} `json:"models"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &l)
		for _, m := range l.Models {
			if m.ID == "llama3.2:1b" && m.Deployed {
				return true
			}
		}
		return false
	})
	if !deployed {
		t.Fatal("model never reported deployed")
	}

	// enable
	if rec := h.do("POST", "/api/v1/models/llama3.2:1b/enable", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("enable: %d (%s)", rec.Code, rec.Body)
	}

	// chat (admin-authed) -> reply from the model
	rec = h.do("POST", "/api/v1/chat", map[string]string{"prompt": "meaning of life?"}, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat: %d (%s)", rec.Code, rec.Body)
	}
	var cr struct {
		Reply string `json:"reply"`
	}
	mustJSON(t, rec, &cr)
	if cr.Reply != "42" {
		t.Fatalf("reply = %q", cr.Reply)
	}
}

// TestActivityReflectsRealTokens proves the dashboard tokens/sec comes from
// actual generations, not the synthesized fallback.
func TestActivityReflectsRealTokens(t *testing.T) {
	h := inferenceHarness(t)
	// deploy + enable a model
	h.do("POST", "/api/v1/models/llama3.2:1b/deploy", nil, adminHdr)
	if !waitForAPI(time.Second, func() bool {
		r := h.do("GET", "/api/v1/models", nil, adminHdr)
		var l struct {
			Models []struct {
				ID       string `json:"id"`
				Deployed bool   `json:"deployed"`
			} `json:"models"`
		}
		_ = json.Unmarshal(r.Body.Bytes(), &l)
		for _, m := range l.Models {
			if m.ID == "llama3.2:1b" && m.Deployed {
				return true
			}
		}
		return false
	}) {
		t.Fatal("model not deployed")
	}
	h.do("POST", "/api/v1/models/llama3.2:1b/enable", nil, adminHdr)

	h.srv.RecordActivity() // baseline (0 tokens so far)
	h.do("POST", "/api/v1/chat", map[string]string{"prompt": "hi"}, adminHdr) // +12 tokens
	h.clock = h.clock.Add(time.Second)
	h.srv.RecordActivity() // 12 tokens / 1s = 12 tok/s

	rec := h.do("GET", "/api/v1/activity", nil, adminHdr)
	var resp activityResp
	mustJSON(t, rec, &resp)
	if resp.TokensPerSec != 12 {
		t.Fatalf("real tokens/sec = %v, want 12", resp.TokensPerSec)
	}
}

func TestChatModelSelection(t *testing.T) {
	h := inferenceHarness(t)
	// deploy two models, then ENABLE both for users (multiple at once)
	for _, id := range []string{"llama3.2:1b", "gemma2:2b"} {
		h.do("POST", "/api/v1/models/"+id+"/deploy", nil, adminHdr)
	}
	for _, id := range []string{"llama3.2:1b", "gemma2:2b"} {
		_ = waitForAPI(time.Second, func() bool {
			return h.do("POST", "/api/v1/models/"+id+"/enable", nil, adminHdr).Code == http.StatusOK
		})
	}

	// the selectable list = the enabled set
	rec := h.do("GET", "/api/v1/chat/models", nil, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("chat models: %d", rec.Code)
	}
	var list struct {
		Models []struct {
			ID, Name string
		} `json:"models"`
	}
	mustJSON(t, rec, &list)
	if len(list.Models) != 2 || list.Models[0].Name == "" {
		t.Fatalf("want 2 named enabled models, got %+v", list.Models)
	}

	// chat selecting an enabled model -> 200
	if rec := h.do("POST", "/api/v1/chat", map[string]any{"model": "gemma2:2b", "prompt": "hi"}, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("model-selected chat: %d (%s)", rec.Code, rec.Body)
	}
	// selecting a model that isn't enabled -> 409
	if rec := h.do("POST", "/api/v1/chat", map[string]any{"model": "qwen2.5:14b", "prompt": "hi"}, adminHdr); rec.Code != http.StatusConflict {
		t.Fatalf("not-enabled select: want 409, got %d", rec.Code)
	}

	// disable one -> the selectable list shrinks to one
	if rec := h.do("POST", "/api/v1/models/gemma2:2b/disable", nil, adminHdr); rec.Code != http.StatusOK {
		t.Fatalf("disable: %d", rec.Code)
	}
	rec = h.do("GET", "/api/v1/chat/models", nil, adminHdr)
	var after struct {
		Models []struct{ ID string } `json:"models"`
	}
	mustJSON(t, rec, &after)
	if len(after.Models) != 1 || after.Models[0].ID != "llama3.2:1b" {
		t.Fatalf("after disable want only llama3.2:1b, got %+v", after.Models)
	}
}

func TestModelsRequiresAdmin(t *testing.T) {
	h := inferenceHarness(t)
	if rec := h.do("GET", "/api/v1/models", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestDeployUnknownModel(t *testing.T) {
	h := inferenceHarness(t)
	if rec := h.do("POST", "/api/v1/models/bogus:1t/deploy", nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestEnableBeforeDeploy(t *testing.T) {
	h := inferenceHarness(t)
	if rec := h.do("POST", "/api/v1/models/qwen2.5:7b/enable", nil, adminHdr); rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestChatEmptyPrompt(t *testing.T) {
	h := inferenceHarness(t)
	if rec := h.do("POST", "/api/v1/chat", map[string]string{"prompt": "  "}, adminHdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// TestChatRequiresAuth: an unauthenticated inference endpoint would let any
// reachable client (or any website in a LAN user's browser) burn the GPU.
func TestChatRequiresAuth(t *testing.T) {
	h := inferenceHarness(t)
	if rec := h.do("POST", "/api/v1/chat", map[string]string{"prompt": "hi"}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("chat without auth: want 401, got %d", rec.Code)
	}
	if rec := h.do("GET", "/api/v1/chat/models", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("chat/models without auth: want 401, got %d", rec.Code)
	}
}

func TestInferenceEndpointsAbsentByDefault(t *testing.T) {
	h := newHarness(t) // no Inference configured
	if rec := h.do("GET", "/api/v1/models", nil, adminHdr); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when inference disabled, got %d", rec.Code)
	}
}

func waitForAPI(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}
