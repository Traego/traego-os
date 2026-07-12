// Package agent implements traegod: the node-side daemon that announces a
// machine to the controller, waits to be adopted, then heartbeats until told
// to leave. It is the thing the `curl | sh` join script installs.
package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

	// CAFingerprint is the hex SHA-256 of the controller CA certificate
	// (printed by the controller at boot). When set, the agent refuses any
	// TLS controller that can't present a chain rooted in that exact CA —
	// protecting the join against interception. When empty, the agent trusts
	// the CA on first use and warns.
	CAFingerprint string
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

	// pinned controller CA (from CAFingerprint match or trust-on-first-use);
	// guarded by a mutex because the HTTP transport verifies concurrently.
	pinMu    sync.Mutex
	pinnedCA *x509.Certificate
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
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	a := &Agent{cfg: cfg, metrics: metrics.NewCollector()}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
		if strings.HasPrefix(cfg.ControllerURL, "https://") {
			// controller serves TLS from its own CA: verify against the
			// pinned fingerprint (or TOFU) instead of the system roots
			cfg.HTTPClient.Transport = &http.Transport{TLSClientConfig: a.tlsConfig(nil)}
		}
	}
	a.http = cfg.HTTPClient
	return a
}

// tlsConfig builds a client TLS config that authenticates the controller via
// the pinned CA rather than system roots/hostnames: homelab controllers are
// reached by arbitrary IPs, so identity comes from the CA — which signs
// ServerAuth certs only for the controller itself.
func (a *Agent) tlsConfig(clientCert *tls.Certificate) *tls.Config {
	cfg := &tls.Config{
		MinVersion:            tls.VersionTLS12,
		InsecureSkipVerify:    true, // verification happens in VerifyPeerCertificate
		VerifyPeerCertificate: a.verifyController,
	}
	if clientCert != nil {
		cfg.Certificates = []tls.Certificate{*clientCert}
	}
	return cfg
}

// verifyController checks the presented chain against the pinned controller
// CA. On the very first connection the CA is selected by CAFingerprint match
// (or trusted-on-first-use with a warning); afterwards it must never change.
func (a *Agent) verifyController(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return fmt.Errorf("controller presented no certificate")
	}
	leaf, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return err
	}
	var chain []*x509.Certificate
	for _, der := range rawCerts[1:] {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return err
		}
		chain = append(chain, c)
	}

	var pinned *x509.Certificate
	for _, c := range chain {
		if !c.IsCA {
			continue
		}
		if p, err := a.pinCA(c); err == nil {
			pinned = p
			break
		}
	}
	if pinned == nil {
		return fmt.Errorf("controller did not present a CA matching the pinned fingerprint")
	}
	roots := x509.NewCertPool()
	roots.AddCert(pinned)
	inter := x509.NewCertPool()
	for _, c := range chain {
		inter.AddCert(c)
	}
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

// pinCA records the controller CA on first sight, enforcing CAFingerprint when
// configured (TOFU with a warning otherwise). Once pinned it never changes for
// the life of the process; a different CA is rejected.
func (a *Agent) pinCA(c *x509.Certificate) (*x509.Certificate, error) {
	a.pinMu.Lock()
	defer a.pinMu.Unlock()
	if a.pinnedCA != nil {
		return a.pinnedCA, nil
	}
	sum := sha256.Sum256(c.Raw)
	fp := hex.EncodeToString(sum[:])
	want := strings.ToLower(strings.ReplaceAll(a.cfg.CAFingerprint, ":", ""))
	if want != "" && fp != want {
		return nil, fmt.Errorf("controller CA fingerprint %s does not match pinned %s", fp, want)
	}
	if want == "" {
		a.cfg.Logf("WARNING: trusting controller CA on first use (fingerprint %s) — set TRAEGO_CA_FINGERPRINT to protect the join against interception", fp)
	}
	a.pinnedCA = c
	return c, nil
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
			// transient (controller restarting, network blip): keep waiting —
			// a pending node's whole job is to outwait the operator
			a.cfg.Logf("credential: %v — retrying", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(a.cfg.PollInterval * 5):
			}
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
		case status == http.StatusTooManyRequests:
			// the controller rate-limits our source IP (someone nearby is
			// failing auth); back off well past the limiter window
			a.cfg.Logf("credential: rate limited — backing off")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(90 * time.Second):
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

	// Anchor trust: over an https control plane the CA is already pinned from
	// the TLS handshake and the response copy is ignored; over a plaintext
	// control plane the response CA is checked against TRAEGO_CA_FINGERPRINT
	// (or trusted on first use with a warning).
	caBlock, _ := pem.Decode([]byte(cr.CA))
	if caBlock == nil {
		return fmt.Errorf("controller returned no CA certificate")
	}
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		return fmt.Errorf("controller CA: %w", err)
	}
	if _, err := a.pinCA(caCert); err != nil {
		return err
	}

	clientCert, err := tls.X509KeyPair([]byte(cr.Certificate), pemKey(key))
	if err != nil {
		return fmt.Errorf("client keypair: %w", err)
	}
	a.secureClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: a.tlsConfig(&clientCert),
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
			a.cfg.Logf("secure mode unavailable, heartbeating over bearer until mTLS succeeds: %v", err)
		}
	}
	if err := a.Heartbeat(ctx); err != nil {
		a.cfg.Logf("initial heartbeat failed: %v", err)
	}
	// mTLS is retried, never abandoned: a node must not silently settle on
	// plaintext bearer heartbeats because of one failed handshake.
	const secureRetryEvery = 6 // heartbeat ticks (~1min at the default interval)
	ticker := time.NewTicker(a.cfg.HeartbeatInterval)
	defer ticker.Stop()
	var ticks, secureFails int
	for {
		select {
		case <-ctx.Done():
			leaveCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.Leave(leaveCtx)
			cancel()
			return ctx.Err()
		case <-ticker.C:
			ticks++
			if a.cfg.Secure && !a.secure && ticks%secureRetryEvery == 0 {
				if err := a.GoSecure(ctx); err != nil {
					a.cfg.Logf("mTLS retry failed: %v", err)
				}
			}
			if err := a.Heartbeat(ctx); err != nil {
				a.cfg.Logf("heartbeat error: %v", err)
				if a.secure {
					if secureFails++; secureFails >= 3 {
						a.secure = false
						secureFails = 0
						a.cfg.Logf("mTLS heartbeats failing, using bearer until mTLS is re-established")
					}
				}
			} else {
				secureFails = 0
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
