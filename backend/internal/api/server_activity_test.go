package api

import (
	"net/http"
	"testing"

	"github.com/traego/traego/internal/store"
)

func TestStepActivityConvergesToTarget(t *testing.T) {
	// one inference node -> target 90 tok/s; from 0 it climbs without overshooting
	cur := ActivitySample{}
	for i := 0; i < 40; i++ {
		cur = stepActivity(cur, 1, 0)
	}
	if cur.TokensPerSec < 89 || cur.TokensPerSec > 90.1 {
		t.Fatalf("did not converge to ~90: %v", cur.TokensPerSec)
	}
	if cur.ActiveJobs != 3 {
		t.Fatalf("jobs = %d, want 3", cur.ActiveJobs)
	}
}

func TestStepActivityZeroNodesDecays(t *testing.T) {
	cur := ActivitySample{TokensPerSec: 90, ActiveJobs: 3}
	for i := 0; i < 40; i++ {
		cur = stepActivity(cur, 0, 0)
	}
	if cur.TokensPerSec > 1 || cur.ActiveJobs != 0 {
		t.Fatalf("did not decay to idle: %+v", cur)
	}
}

func TestStepActivityNeverNegative(t *testing.T) {
	cur := stepActivity(ActivitySample{TokensPerSec: 5}, 0, -1000)
	if cur.TokensPerSec < 0 {
		t.Fatalf("tps went negative: %v", cur.TokensPerSec)
	}
}

func TestRecordActivityAndEndpoint(t *testing.T) {
	h := newHarness(t)
	// an online inference node drives the signal up
	a := h.announce(t, "infer")
	h.do("POST", "/api/v1/nodes/"+a.ID+"/adopt", adoptReq{PairingCode: a.PairingCode, Role: store.RoleInference}, adminHdr)
	var cr credentialResp
	mustJSON(t, h.do("POST", "/api/v1/nodes/"+a.ID+"/credential", credentialReq{EnrollSecret: a.EnrollSecret}, nil), &cr)
	h.do("POST", "/api/v1/nodes/"+a.ID+"/heartbeat", nil, bearerHdr(cr.Credential))

	for i := 0; i < 5; i++ {
		h.srv.RecordActivity()
	}
	rec := h.do("GET", "/api/v1/activity", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("activity: %d", rec.Code)
	}
	var resp activityResp
	mustJSON(t, rec, &resp)
	if len(resp.History) != 5 {
		t.Fatalf("history length = %d, want 5", len(resp.History))
	}
	if resp.TokensPerSec <= 0 || resp.ActiveJobs != 3 {
		t.Fatalf("expected live signal with an inference node: %+v", resp)
	}
}

func TestRecordActivityHistoryCaps(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < activityCap+25; i++ {
		h.srv.RecordActivity()
	}
	rec := h.do("GET", "/api/v1/activity", nil, nil)
	var resp activityResp
	mustJSON(t, rec, &resp)
	if len(resp.History) != activityCap {
		t.Fatalf("history not capped: %d, want %d", len(resp.History), activityCap)
	}
}
