package provision

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func post(t *testing.T, h http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provision", bytes.NewReader(buf))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestProvisionOneShot(t *testing.T) {
	var applied *Claim
	claimed := false
	h := New(Config{
		ClaimCode: "7GK4-PW2N",
		Claimed:   func() bool { return claimed },
		Apply: func(c Claim) error {
			applied = &c
			claimed = true
			return nil
		},
		Logf: t.Logf,
	})

	// wrong code: rejected, nothing applied
	rec := post(t, h, map[string]string{"claim_code": "AAAA-0000", "hub_url": "http://hub:9443", "org_token": "tok"})
	if rec.Code != http.StatusForbidden || applied != nil {
		t.Fatalf("wrong code: got %d applied=%v, want 403 nil", rec.Code, applied)
	}

	// missing hub config: rejected
	rec = post(t, h, map[string]string{"claim_code": "7GK4-PW2N"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing fields: got %d, want 400", rec.Code)
	}

	// correct claim succeeds and applies
	rec = post(t, h, map[string]string{"claim_code": "7GK4-PW2N", "hub_url": "http://hub:9443", "org_token": "tok", "name": "garage"})
	if rec.Code != http.StatusOK || applied == nil || applied.HubURL != "http://hub:9443" || applied.Name != "garage" {
		t.Fatalf("claim: got %d applied=%+v", rec.Code, applied)
	}

	// second claim is refused — one-shot
	rec = post(t, h, map[string]string{"claim_code": "7GK4-PW2N", "hub_url": "http://other:9443", "org_token": "tok2"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-claim: got %d, want 409", rec.Code)
	}
}

func TestNewClaimCodeShape(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c := NewClaimCode()
		if len(c) != 9 || c[4] != '-' {
			t.Fatalf("bad code shape: %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 45 {
		t.Fatalf("codes look non-random: %d unique of 50", len(seen))
	}
}
