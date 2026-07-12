// Package ca is the controller's internal certificate authority. It mints the
// controller's own TLS server certificate and signs node certificate-signing
// requests into short-lived client certificates, giving every adopted node a
// cryptographic identity for the mTLS data plane.
package ca

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"time"
)

// CA is a self-signed root that signs server and node certificates.
type CA struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM []byte
}

// New generates a fresh CA (ECDSA P-256). Persist it with CertPEM/KeyPEM and
// reload with Load so node certificates survive controller restarts.
func New() (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Traego Controller CA", Organization: []string{"Traego"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key, certPEM: pemBlock("CERTIFICATE", der)}, nil
}

// CertPEM returns the CA certificate in PEM form (safe to hand out publicly so
// clients can trust the controller and verify node certs).
func (c *CA) CertPEM() []byte { return c.certPEM }

// KeyPEM returns the CA private key as PKCS#8 PEM. This is the root secret:
// persist it only to the controller's own store, never serve it.
func (c *CA) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		return nil, err
	}
	return pemBlock("PRIVATE KEY", der), nil
}

// Fingerprint is the SHA-256 of the CA certificate (DER), hex-encoded. Shown at
// boot so operators can pin it on nodes joining over an untrusted network.
func (c *CA) Fingerprint() string {
	sum := sha256.Sum256(c.cert.Raw)
	return hex.EncodeToString(sum[:])
}

// Load reconstructs a CA from PEM produced by CertPEM and KeyPEM.
func Load(certPEM, keyPEM []byte) (*CA, error) {
	cb, _ := pem.Decode(certPEM)
	if cb == nil || cb.Type != "CERTIFICATE" {
		return nil, errors.New("ca: not a PEM certificate")
	}
	cert, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse cert: %w", err)
	}
	if !cert.IsCA {
		return nil, errors.New("ca: certificate is not a CA")
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil || kb.Type != "PRIVATE KEY" {
		return nil, errors.New("ca: not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse key: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("ca: key is not ECDSA")
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return nil, errors.New("ca: key does not match certificate")
	}
	return &CA{cert: cert, key: key, certPEM: pemBlock("CERTIFICATE", cb.Bytes)}, nil
}

// Pool returns a cert pool containing the CA, for verifying peers.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// ServerCertificate mints a TLS server certificate for the given hostnames/IPs,
// signed by the CA. Used for the controller's own mTLS listener.
func (c *CA) ServerCertificate(hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: "traego-controller"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, _ := x509.ParseCertificate(der)
	// Serve the CA in the chain so clients pinning the CA fingerprint can
	// verify without a separate fetch.
	return tls.Certificate{Certificate: [][]byte{der, c.cert.Raw}, PrivateKey: key, Leaf: leaf}, nil
}

// SignCSR verifies a PEM-encoded certificate-signing request and issues a client
// certificate whose CommonName is forced to nodeID (so a node can never request
// an identity other than its own). ttl bounds the cert lifetime.
func (c *CA) SignCSR(csrPEM []byte, nodeID string, ttl time.Duration) ([]byte, error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return nil, errors.New("ca: not a PEM certificate request")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ca: parse csr: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("ca: csr signature: %w", err)
	}
	if err := checkCSRKey(csr); err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: nodeID, Organization: []string{"Traego Node"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(ttl),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, csr.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	return pemBlock("CERTIFICATE", der), nil
}

// checkCSRKey restricts issued certs to modern keys: nodes generate P-256 via
// NewKeyAndCSR, so anything else in a CSR is at best a foreign client and at
// worst an attempt to get a weak key signed by our root.
func checkCSRKey(csr *x509.CertificateRequest) error {
	switch pub := csr.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if pub.Curve != elliptic.P256() && pub.Curve != elliptic.P384() {
			return errors.New("ca: unsupported ECDSA curve")
		}
		return nil
	case ed25519.PublicKey:
		return nil
	default:
		return fmt.Errorf("ca: unsupported key type %T (want ECDSA P-256/P-384 or Ed25519)", pub)
	}
}

// NewKeyAndCSR generates a P-256 key and a PEM-encoded certificate-signing
// request with the given CommonName. The caller (a node) keeps the private key
// and sends the CSR to the controller to be signed.
func NewKeyAndCSR(commonName string) (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, key)
	if err != nil {
		return nil, nil, err
	}
	return key, pemBlock("CERTIFICATE REQUEST", der), nil
}

func serial() *big.Int {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, _ := rand.Int(rand.Reader, max)
	return n
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}
