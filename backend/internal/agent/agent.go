// Package agent implements traegod: the node-side daemon that announces a
// machine to the controller, waits to be adopted, then heartbeats until told
// to leave. It is the thing the `curl | sh` join script installs.
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/traego/traego/internal/ca"
	"github.com/traego/traego/internal/metrics"
	"github.com/traego/traego/internal/store"
)

// Config configures an Agent. ControllerURL, JoinToken and Name are required.
type Config struct {
	ControllerURL     string
	JoinToken         string
	Name              string
	Site              string // the site (location) this node belongs to
	Class             store.Class
	Specs             store.Specs
	HeartbeatInterval time.Duration
	PollInterval      time.Duration // how often to poll for adoption
	HTTPClient        *http.Client
	Logf              func(format string, args ...any)

	// Secure, when set, makes the node request a CA-signed certificate after
	// adoption and heartbeat over mTLS to SecureURL instead of the bearer API.
	Secure    bool
	SecureURL string
}

// Agent drives one node through its lifecycle. Methods are intended to be
// called from a single goroutine (Run does exactly that).
type Agent struct {
	cfg     Config
	http    *http.Client
	metrics *metrics.Collector

	id           string
	enrollSecret string
	pairingCode  string
	credential   string
	role         store.Role

	secure       bool
	secureClient *http.Client
}

// New builds an Agent with defaults applied.
func New(cfg Config) *Agent {
	if cfg.Class == "" {
		cfg.Class = store.ClassPersistent
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 10 * time.Second
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Agent{cfg: cfg, http: cfg.HTTPClient, metrics: metrics.NewCollector()}
}

// Accessors (mainly for tests and the CLI status line).
func (a *Agent) ID() string          { return a.id }
func (a *Agent) PairingCode() string { return a.pairingCode }
func (a *Agent) Credential() string  { return a.credential }
func (a *Agent) Role() store.Role    { return a.role }
func (a *Agent) Secured() bool       { return a.secure }

type announceReq struct {
	Name  string      `json:"name"`
	Site  string      `json:"site"`
	Class store.Class `json:"class"`
	Specs store.Specs `json:"specs"`
}
type announceResp struct {
	ID           string      `json:"id"`
	EnrollSecret string      `json:"enroll_secret"`
	PairingCode  string      `json:"pairing_code"`
	State        store.State `json:"state"`
}
type credentialReq struct {
	EnrollSecret string `json:"enroll_secret"`
}
type credentialResp struct {
	Credential string      `json:"credential"`
	Role       store.Role  `json:"role"`
	State      store.State `json:"state"`
}
type heartbeatReq struct {
	Metrics *metrics.Sample `json:"metrics"`
}
type heartbeatResp struct {
	Role  store.Role  `json:"role"`
	State store.State `json:"state"`
}

// Announce registers the node and records the enroll secret + pairing code.
// The pairing code is printed so the operator can confirm it during adoption.
func (a *Agent) Announce(ctx context.Context) error {
	var resp announceResp
	status, err := a.post(ctx, a.cfg.ControllerURL+"/api/v1/discovery/announce",
		announceReq{Name: a.cfg.Name, Site: a.cfg.Site, Class: a.cfg.Class, Specs: a.cfg.Specs},
		map[string]string{"X-Traego-Join-Token": a.cfg.JoinToken}, &resp)
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	if status != http.StatusCreated {
		return fmt.Errorf("announce: unexpected status %d", status)
	}
	a.id, a.enrollSecret, a.pairingCode = resp.ID, resp.EnrollSecret, resp.PairingCode
	a.cfg.Logf("announced as %s — pairing code: %s (confirm this in the controller to adopt)", a.id, a.pairingCode)
	return nil
}

// AwaitCredential polls until the node is adopted, then stores its credential.
// It returns when adopted or when ctx is cancelled.
func (a *Agent) AwaitCredential(ctx context.Context) error {
	for {
		var resp credentialResp
		status, err := a.post(ctx, a.cfg.ControllerURL+"/api/v1/nodes/"+a.id+"/credential",
			credentialReq{EnrollSecret: a.enrollSecret}, nil, &resp)
		switch {
		case err != nil:
			return fmt.Errorf("credential: %w", err)
		case status == http.StatusOK:
			a.credential, a.role = resp.Credential, resp.Role
			a.cfg.Logf("adopted as role=%s — credential issued", a.role)
			return nil
		case status == http.StatusConflict:
			// not adopted yet; wait and retry
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(a.cfg.PollInterval):
			}
		default:
			return fmt.Errorf("credential: unexpected status %d", status)
		}
	}
}

// GoSecure obtains a CA-signed client certificate and stands up an mTLS client.
// After this, heartbeats use mutual TLS (the cert proves identity, no bearer).
func (a *Agent) GoSecure(ctx context.Context) error {
	key, csrPEM, err := ca.NewKeyAndCSR(a.id)
	if err != nil {
		return fmt.Errorf("csr: %w", err)
	}
	var cr struct {
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
	}
	status, err := a.doJSON(ctx, a.http, http.MethodPost,
		a.cfg.ControllerURL+"/api/v1/nodes/"+a.id+"/certificate", csrPEM, a.authHeader(), &cr)
	if err != nil {
		return fmt.Errorf("certificate: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("certificate: unexpected status %d", status)
	}

	clientCert, err := tls.X509KeyPair([]byte(cr.Certificate), pemKey(key))
	if err != nil {
		return fmt.Errorf("client keypair: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(cr.CA)) {
		return fmt.Errorf("could not trust controller CA")
	}
	a.secureClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{clientCert}},
	}}

	// confirm the mutual handshake works and the controller verifies our identity
	var who struct {
		NodeID  string `json:"node_id"`
		Secured bool   `json:"secured"`
	}
	st, err := a.doJSON(ctx, a.secureClient, http.MethodGet, a.cfg.SecureURL+"/api/v1/secure/whoami", nil, nil, &who)
	if err != nil || st != http.StatusOK {
		return fmt.Errorf("mTLS whoami failed (status %d): %w", st, err)
	}
	a.secure = true
	if leaf := clientCert.Leaf; leaf != nil {
		a.cfg.Logf("mTLS established — controller verified identity as %s (cert serial %x, expires %s)",
			who.NodeID, leaf.SerialNumber, leaf.NotAfter.Format(time.RFC3339))
	} else {
		a.cfg.Logf("mTLS established — controller verified identity as %s", who.NodeID)
	}
	return nil
}

// Heartbeat sends a single liveness ping over mTLS if secured, else the bearer API.
func (a *Agent) Heartbeat(ctx context.Context) error {
	url := a.cfg.ControllerURL + "/api/v1/nodes/" + a.id + "/heartbeat"
	headers := a.authHeader()
	client := a.http
	if a.secure && a.secureClient != nil {
		url = a.cfg.SecureURL + "/api/v1/nodes/" + a.id + "/heartbeat"
		headers = nil
		client = a.secureClient
	}
	sample := a.metrics.Sample()
	body := heartbeatReq{Metrics: &sample}
	var resp heartbeatResp
	status, err := a.doJSON(ctx, client, http.MethodPost, url, body, headers, &resp)
	if err != nil {
		return fmt.Errorf("heartbeat: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("heartbeat: unexpected status %d", status)
	}
	a.role = resp.Role
	return nil
}

func pemKey(key *ecdsa.PrivateKey) []byte {
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// Leave tells the controller the node is departing (best effort).
func (a *Agent) Leave(ctx context.Context) error {
	status, err := a.post(ctx, a.cfg.ControllerURL+"/api/v1/nodes/"+a.id+"/leave",
		nil, a.authHeader(), nil)
	if err != nil {
		return fmt.Errorf("leave: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("leave: unexpected status %d", status)
	}
	a.cfg.Logf("left the pool cleanly")
	return nil
}

// Run executes the full lifecycle: announce, await adoption, then heartbeat
// until ctx is cancelled, at which point it drains with a graceful Leave.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Announce(ctx); err != nil {
		return err
	}
	if err := a.AwaitCredential(ctx); err != nil {
		return err
	}
	if a.cfg.Secure {
		if err := a.GoSecure(ctx); err != nil {
			a.cfg.Logf("secure mode unavailable, falling back to bearer heartbeats: %v", err)
		}
	}
	if err := a.Heartbeat(ctx); err != nil {
		a.cfg.Logf("initial heartbeat failed: %v", err)
	}
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.Leave(leaveCtx)
			cancel()
			return ctx.Err()
		case <-ticker.C:
			if err := a.Heartbeat(ctx); err != nil {
				a.cfg.Logf("heartbeat error: %v", err)
			}
		}
	}
}

func (a *Agent) authHeader() map[string]string {
	return map[string]string{"Authorization": "Bearer " + a.credential}
}

// DetectSpecs gathers host hardware. CPU cores come from the runtime; memory
// and GPU VRAM are read from env so containers/tests can declare them.
func DetectSpecs() store.Specs {
	s := store.Specs{CPUCores: runtime.NumCPU()}
	if v, err := strconv.Atoi(os.Getenv("TRAEGO_MEMORY_GB")); err == nil {
		s.MemoryGB = v
	}
	if v, err := strconv.Atoi(os.Getenv("TRAEGO_GPU_VRAM_GB")); err == nil {
		s.GPUVRAMGB = v
	}
	return s
}

// post issues a JSON POST via the default client.
func (a *Agent) post(ctx context.Context, url string, body any, headers map[string]string, out any) (int, error) {
	return a.doJSON(ctx, a.http, http.MethodPost, url, body, headers, out)
}

// doJSON sends a request on the given client. A []byte body is sent raw (used
// for CSR PEM); anything else is JSON-encoded. A 2xx body is decoded into out.
func (a *Agent) doJSON(ctx context.Context, client *http.Client, method, url string, body any, headers map[string]string, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rdr = bytes.NewReader(b)
		default:
			raw, err := json.Marshal(body)
			if err != nil {
				return 0, err
			}
			rdr = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode/100 == 2 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}
