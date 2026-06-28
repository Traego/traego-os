package api

import (
	"net/http"
	"testing"

	"github.com/traego/traego/internal/store"
)

func TestSitesListIncludesHome(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/api/v1/sites", nil, adminHdr)
	if rec.Code != http.StatusOK {
		t.Fatalf("sites: %d", rec.Code)
	}
	var out struct {
		Sites []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsHome    bool   `json:"is_home"`
			NodeCount int    `json:"node_count"`
		} `json:"sites"`
		Home string `json:"home"`
	}
	mustJSON(t, rec, &out)
	if out.Home != "local" || len(out.Sites) != 1 || !out.Sites[0].IsHome {
		t.Fatalf("expected the home site present + flagged: %+v", out)
	}
}

func TestSitesRequireAdmin(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/api/v1/sites", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestAnnounceDeclaresAndAutoRegistersSite(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/discovery/announce",
		announceReq{Name: "branch-node", Site: "branch", Specs: store.Specs{CPUCores: 4}}, joinHdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("announce: %d", rec.Code)
	}
	var a announceResp
	mustJSON(t, rec, &a)

	// node carries the site
	n, _ := h.st.Get(a.ID)
	if n.Site != "branch" {
		t.Fatalf("node site = %q, want branch", n.Site)
	}
	// the site was auto-registered and shows the node count
	var out struct {
		Sites []struct {
			ID        string `json:"id"`
			NodeCount int    `json:"node_count"`
		} `json:"sites"`
	}
	mustJSON(t, h.do("GET", "/api/v1/sites", nil, adminHdr), &out)
	var found bool
	for _, s := range out.Sites {
		if s.ID == "branch" {
			found = true
			if s.NodeCount != 1 {
				t.Fatalf("branch node_count = %d, want 1", s.NodeCount)
			}
		}
	}
	if !found {
		t.Fatalf("branch site not auto-registered: %+v", out.Sites)
	}
}

func TestAnnounceDefaultsToHomeSite(t *testing.T) {
	h := newHarness(t)
	rec := h.do("POST", "/api/v1/discovery/announce", announceReq{Name: "x", Specs: store.Specs{CPUCores: 2}}, joinHdr)
	var a announceResp
	mustJSON(t, rec, &a)
	if n, _ := h.st.Get(a.ID); n.Site != "local" {
		t.Fatalf("default site = %q, want local", n.Site)
	}
}

func TestCreateSite(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("POST", "/api/v1/sites", map[string]string{"name": "Branch Office"}, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("create without admin: want 401, got %d", rec.Code)
	}
	if rec := h.do("POST", "/api/v1/sites", map[string]string{"name": " "}, adminHdr); rec.Code != http.StatusBadRequest {
		t.Fatalf("blank name: want 400, got %d", rec.Code)
	}
	rec := h.do("POST", "/api/v1/sites", map[string]string{"name": "Branch Office"}, adminHdr)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	var site store.Site
	mustJSON(t, rec, &site)
	if site.Name != "Branch Office" || site.ID == "" {
		t.Fatalf("bad created site: %+v", site)
	}
}
