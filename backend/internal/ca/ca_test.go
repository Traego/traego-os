package ca

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSignCSRIssuesVerifiableClientCert(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, csr, err := NewKeyAndCSR("requested-cn")
	if err != nil {
		t.Fatalf("csr: %v", err)
	}
	certPEM, err := c.SignCSR(csr, "n-abc123", time.Hour)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}
	block, _ := pem.Decode(certPEM)
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	// CommonName is forced to the node id, not whatever the CSR asked for
	if leaf.Subject.CommonName != "n-abc123" {
		t.Fatalf("CN = %q, want forced node id", leaf.Subject.CommonName)
	}
	// chains to the CA, usable as a client cert
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     c.Pool(),
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client cert does not verify against CA: %v", err)
	}
}

func TestSignCSRRejectsGarbage(t *testing.T) {
	c, _ := New()
	if _, err := c.SignCSR([]byte("not a pem"), "n-1", time.Hour); err == nil {
		t.Fatal("expected error on non-PEM input")
	}
	// a PEM block of the wrong type
	wrong := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}})
	if _, err := c.SignCSR(wrong, "n-1", time.Hour); err == nil {
		t.Fatal("expected error on wrong PEM type")
	}
}

// TestMutualTLSHandshake proves the whole point: a server using the CA-signed
// server cert + requiring client certs accepts a node carrying a CA-signed
// client cert, and rejects a client with no cert.
func TestMutualTLSHandshake(t *testing.T) {
	c, _ := New()
	serverCert, err := c.ServerCertificate([]string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("server cert: %v", err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientCAs:    c.Pool(),
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}
	srv.StartTLS()
	defer srv.Close()

	// issue a client cert for a node
	key, csr, _ := NewKeyAndCSR("node")
	certPEM, err := c.SignCSR(csr, "n-secure-1", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	keyPEM := marshalKey(t, key)
	clientCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 keypair: %v", err)
	}

	// with the client cert -> handshake succeeds, server sees our CN
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:      c.Pool(),
		Certificates: []tls.Certificate{clientCert},
	}}}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("mTLS request failed: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "n-secure-1" {
		t.Fatalf("server saw CN %q, want n-secure-1", body)
	}

	// without a client cert -> rejected
	noCert := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: c.Pool()}}}
	if _, err := noCert.Get(srv.URL); err == nil {
		t.Fatal("expected handshake to fail without a client cert")
	}
}

func TestCertPEMAndPool(t *testing.T) {
	c, _ := New()
	if block, _ := pem.Decode(c.CertPEM()); block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("CertPEM is not a CERTIFICATE PEM block")
	}
	if c.Pool() == nil {
		t.Fatal("nil pool")
	}
}

func marshalKey(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// TestLoadRoundTrip proves a persisted CA is the same authority after reload:
// certs signed before a "restart" still verify, and the reloaded CA can keep
// signing certs that verify against pools built from the original.
func TestLoadRoundTrip(t *testing.T) {
	orig, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	keyPEM, err := orig.KeyPEM()
	if err != nil {
		t.Fatalf("KeyPEM: %v", err)
	}
	_, csr, _ := NewKeyAndCSR("n-1")
	before, err := orig.SignCSR(csr, "n-1", time.Hour)
	if err != nil {
		t.Fatalf("SignCSR: %v", err)
	}

	reloaded, err := Load(orig.CertPEM(), keyPEM)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.Fingerprint() != orig.Fingerprint() {
		t.Fatalf("fingerprint changed across reload: %s != %s", reloaded.Fingerprint(), orig.Fingerprint())
	}
	// a cert issued before the restart verifies against the reloaded pool
	block, _ := pem.Decode(before)
	leaf, _ := x509.ParseCertificate(block.Bytes)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: reloaded.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("pre-restart cert no longer verifies: %v", err)
	}
	// and the reloaded CA issues certs the original pool trusts
	_, csr2, _ := NewKeyAndCSR("n-2")
	after, err := reloaded.SignCSR(csr2, "n-2", time.Hour)
	if err != nil {
		t.Fatalf("reloaded SignCSR: %v", err)
	}
	block2, _ := pem.Decode(after)
	leaf2, _ := x509.ParseCertificate(block2.Bytes)
	if _, err := leaf2.Verify(x509.VerifyOptions{Roots: orig.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("post-restart cert does not verify against original pool: %v", err)
	}
}

func TestLoadRejectsMismatchedKey(t *testing.T) {
	a, _ := New()
	b, _ := New()
	bKey, _ := b.KeyPEM()
	if _, err := Load(a.CertPEM(), bKey); err == nil {
		t.Fatal("expected error loading cert with a different CA's key")
	}
}

// TestSignCSRRejectsWeakKey: the root must never sign key types nodes don't
// generate (e.g. RSA), closing the door on weak-key certificates.
func TestSignCSRRejectsWeakKey(t *testing.T) {
	c, _ := New()
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "n-evil"},
	}, rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if _, err := c.SignCSR(csr, "n-evil", time.Hour); err == nil {
		t.Fatal("expected RSA CSR to be rejected")
	}
}
