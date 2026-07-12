package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/store"
)

func TestSystemEndpoint(t *testing.T) {
	srv, err := New(Config{
		Store: store.NewMemory(), AdminKey: "k", JoinToken: "j",
		System: func() metrics.Sample {
			return metrics.Sample{Hostname: "ctl-1", Cores: 8, CPUPercent: 42, MemUsedGB: 4, MemTotalGB: 16}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/system", nil)
	req.Header.Set("Authorization", "Bearer k")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("system: %d", rec.Code)
	}
	// and without the admin key, host metrics are not disclosed
	anon := httptest.NewRecorder()
	srv.Handler().ServeHTTP(anon, httptest.NewRequest("GET", "/api/v1/system", nil))
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("system without auth: want 401, got %d", anon.Code)
	}
	var s metrics.Sample
	mustJSON(t, rec, &s)
	if s.Hostname != "ctl-1" || s.Cores != 8 || s.CPUPercent != 42 {
		t.Fatalf("unexpected system sample: %+v", s)
	}
}

func TestSystemEndpointAbsentByDefault(t *testing.T) {
	h := newHarness(t) // no System configured
	if rec := h.do("GET", "/api/v1/system", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when System unset, got %d", rec.Code)
	}
}

func TestHeartbeatStoresMetrics(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "metric-node")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)
	var cr credentialResp
	mustJSON(t, h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil), &cr)

	body := heartbeatReq{Metrics: &metrics.Sample{CPUPercent: 37.5, MemUsedGB: 6, MemTotalGB: 32, Cores: 12}}
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", body, bearerHdr(cr.Credential))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat: %d", rec.Code)
	}
	n, _ := h.st.Get(a.ID)
	if n.Metrics == nil || n.Metrics.CPUPercent != 37.5 || n.Metrics.Cores != 12 {
		t.Fatalf("metrics not stored: %+v", n.Metrics)
	}
}

func TestHeartbeatWithoutMetricsStillWorks(t *testing.T) {
	h := newHarness(t)
	a := h.announce(t, "no-metric-node")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleApp}, adminHdr)
	var cr credentialResp
	mustJSON(t, h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil), &cr)

	// nil body — no metrics
	rec := h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential))
	if rec.Code != http.StatusOK {
		t.Fatalf("heartbeat without metrics: %d", rec.Code)
	}
}
