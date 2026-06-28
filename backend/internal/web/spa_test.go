package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, string(body)
}

func TestSPAHandlerServesAndFallsBack(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html>TRAEGO-APP")},
		"assets/app.js": {Data: []byte("console.log('hi')")},
	}
	h := SPAHandler(fsys)

	// root -> index.html
	if code, body := get(t, h, "/"); code != 200 || !strings.Contains(body, "TRAEGO-APP") {
		t.Fatalf("root: %d %q", code, body)
	}
	// real asset is served as-is
	if code, body := get(t, h, "/assets/app.js"); code != 200 || !strings.Contains(body, "console.log") {
		t.Fatalf("asset: %d %q", code, body)
	}
	// deep client-side route -> falls back to index.html
	if code, body := get(t, h, "/fleet"); code != 200 || !strings.Contains(body, "TRAEGO-APP") {
		t.Fatalf("deep route fallback: %d %q", code, body)
	}
	// a path that traverses still falls back, never escapes
	if code, _ := get(t, h, "/../secrets"); code != 200 {
		t.Fatalf("traversal: %d", code)
	}
}

func TestSPAHandlerNoFrontend(t *testing.T) {
	h := SPAHandler(fstest.MapFS{}) // nothing built
	if code, _ := get(t, h, "/"); code != http.StatusNotFound {
		t.Fatalf("want 404 with no index.html, got %d", code)
	}
}

func TestDistFS(t *testing.T) {
	// the embedded FS is always openable (at least the .gitkeep placeholder)
	if _, err := DistFS().Open("."); err != nil {
		t.Fatalf("DistFS root not openable: %v", err)
	}
}
