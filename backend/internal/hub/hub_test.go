package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testServer(t *testing.T, now *time.Time) *httptest.Server {
	t.Helper()
	n := 0
	srv := New(Config{
		OrgToken: "org-secret",
		Store:    NewMemory(),
		Logf:     t.Logf,
		Now:      func() time.Time { return *now },
		NewID:    func() string { n++; return "c-test" + string(rune('0'+n)) },
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func do(t *testing.T, method, url, token string, body any) (*http.Response, []byte) {
	t.Helper()
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(buf))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	_, _ = out.ReadFrom(resp.Body)
	return resp, out.Bytes()
}

func TestEnrollReportList(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	ts := testServer(t, &now)

	// wrong token is rejected everywhere
	resp, _ := do(t, "POST", ts.URL+"/api/v1/enroll", "nope", map[string]string{"name": "x"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: got %d, want 401", resp.StatusCode)
	}

	// enroll
	resp, body := do(t, "POST", ts.URL+"/api/v1/enroll", "org-secret", map[string]string{"name": "garage"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll: got %d: %s", resp.StatusCode, body)
	}
	var enr struct{ ID string }
	_ = json.Unmarshal(body, &enr)
	if enr.ID == "" {
		t.Fatal("enroll returned no id")
	}

	// re-enroll with the same id keeps identity
	resp, body = do(t, "POST", ts.URL+"/api/v1/enroll", "org-secret", map[string]string{"id": enr.ID, "name": "garage"})
	var re struct{ ID string }
	_ = json.Unmarshal(body, &re)
	if resp.StatusCode != http.StatusOK || re.ID != enr.ID {
		t.Fatalf("re-enroll: got %d id=%q, want 200 id=%q", resp.StatusCode, re.ID, enr.ID)
	}

	// report a summary
	resp, _ = do(t, "POST", ts.URL+"/api/v1/controllers/"+enr.ID+"/report", "org-secret",
		map[string]any{"sites": []SiteSummary{{ID: "local", Name: "Local site", Machines: 2, Online: 2}}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("report: got %d", resp.StatusCode)
	}

	// list: fresh report -> online
	resp, body = do(t, "GET", ts.URL+"/api/v1/controllers", "org-secret", nil)
	var list struct{ Controllers []*Controller }
	_ = json.Unmarshal(body, &list)
	if resp.StatusCode != http.StatusOK || len(list.Controllers) != 1 {
		t.Fatalf("list: got %d with %d controllers", resp.StatusCode, len(list.Controllers))
	}
	c := list.Controllers[0]
	if !c.Online || len(c.Sites) != 1 || c.Sites[0].Machines != 2 {
		t.Fatalf("list entry wrong: %+v", c)
	}

	// stale controller shows offline but stays listed (index survives outages)
	now = now.Add(5 * time.Minute)
	_, body = do(t, "GET", ts.URL+"/api/v1/controllers", "org-secret", nil)
	_ = json.Unmarshal(body, &list)
	if list.Controllers[0].Online {
		t.Fatal("stale controller still marked online")
	}

	// report against an unknown id demands enrollment
	resp, _ = do(t, "POST", ts.URL+"/api/v1/controllers/c-ghost/report", "org-secret", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ghost report: got %d, want 404", resp.StatusCode)
	}
}
