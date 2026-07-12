//go:build integration

// Package deploy holds the docker-compose join harness test. It is gated behind
// the `integration` build tag (and needs Docker), so plain `go test ./...`
// skips it. Run with: go test ./deploy/... -tags=integration
package deploy

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"os/exec"
	"testing"
	"time"
)

const (
	controllerURL = "https://127.0.0.1:18443" // compose maps 18443 -> controller 8443
	adminKey      = "dev-admin-key"
)

// client skips TLS verification: this is the throwaway dev harness talking to
// a compose controller with a freshly generated CA. Real deployments verify by
// pinning the CA fingerprint the controller prints at boot.
var client = &http.Client{Transport: &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
}}

type node struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	State       string `json:"state"`
	Class       string `json:"class"`
	Role        string `json:"role"`
	PairingCode string `json:"pairing_code"`
	Specs       struct {
		GPUVRAMGB int `json:"gpu_vram_gb"`
	} `json:"specs"`
}

func TestComposeJoinFlow(t *testing.T) {
	compose(t, "up", "-d", "--build")
	t.Cleanup(func() { compose(t, "down", "-v") })

	// 1. controller becomes healthy
	if !waitFor(90*time.Second, func() bool {
		resp, err := client.Get(controllerURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}) {
		t.Fatal("controller never became healthy")
	}

	// 2. all three machines announce and appear as pending
	var nodes []node
	if !waitFor(60*time.Second, func() bool {
		nodes = listNodes(t)
		return len(nodes) >= 3
	}) {
		t.Fatalf("expected 3 announced nodes, got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.State != "pending" || n.PairingCode == "" {
			t.Fatalf("node %s not pending with a pairing code: %+v", n.Name, n)
		}
	}

	// 3. adopt each: GPU box -> inference, others -> app/storage
	roles := []string{"app", "storage"}
	ri := 0
	for _, n := range nodes {
		role := "inference"
		if n.Specs.GPUVRAMGB == 0 {
			role = roles[ri%len(roles)]
			ri++
		}
		adopt(t, n.ID, n.PairingCode, role)
	}

	// 4. every node heartbeats and reaches online
	if !waitFor(30*time.Second, func() bool {
		online := 0
		for _, n := range listNodes(t) {
			if n.State == "online" {
				online++
			}
		}
		return online >= 3
	}) {
		t.Fatalf("nodes did not all come online: %+v", listNodes(t))
	}
}

func compose(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"compose", "-f", "compose.yml"}, args...)
	cmd := exec.Command("docker", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
}

func listNodes(t *testing.T) []node {
	t.Helper()
	req, _ := http.NewRequest("GET", controllerURL+"/api/v1/nodes", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Nodes []node `json:"nodes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Nodes
}

func adopt(t *testing.T, id, code, role string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"pairing_code": code, "role": role})
	req, _ := http.NewRequest("POST", controllerURL+"/api/v1/nodes/"+id+"/adopt", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("adopt %s: %v", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("adopt %s: status %d", id, resp.StatusCode)
	}
}

func waitFor(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return cond()
}
