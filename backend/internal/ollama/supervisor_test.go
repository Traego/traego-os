package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManageable(t *testing.T) {
	cases := map[string]bool{
		"http://localhost:11434":  true,
		"http://127.0.0.1:11434":  true,
		"http://[::1]:11434":      true,
		"http://gpu-box:11434":    false,
		"http://192.168.1.5:1234": false,
		"::not a url::":           false,
	}
	for url, want := range cases {
		if got := Manageable(url); got != want {
			t.Errorf("Manageable(%q) = %v, want %v", url, got, want)
		}
	}
}

// An already-healthy backend is external: EnsureUp must succeed without
// spawning anything, and Stop must not touch it.
func TestSupervisorLeavesExternalBackendAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sup := NewSupervisor(New(srv.URL), t.Logf)
	if err := sup.EnsureUp(context.Background()); err != nil {
		t.Fatalf("EnsureUp against healthy external backend: %v", err)
	}
	if sup.Managed() {
		t.Fatal("supervisor claims to manage a backend it did not start")
	}
	sup.Stop() // must be a no-op, not a panic
}
