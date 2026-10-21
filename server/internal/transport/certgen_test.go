package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCertWithExpiry writes a self-signed cert/key pair whose validity
// window is fully under the caller's control, so a test can place NotAfter
// in the past without waiting out GenerateSelfSigned's real ~13-day window.
func writeCertWithExpiry(t *testing.T, certPath, keyPath string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    notAfter.Add(-48 * time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}

// TestLoadOrGenerateCert_RegeneratesExpiredCert pins the fix for the local
// dev incident (2026-10-21): an expired on-disk cert used to be loaded as-is
// forever (loadOrGenerateCert only checked file presence, never NotAfter),
// producing a silent WebTransport handshake failure with no server-side log
// of the cause -- the identical symptom as the unrelated production
// cert-mount incident the same week.
func TestLoadOrGenerateCert_RegeneratesExpiredCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeCertWithExpiry(t, certPath, keyPath, time.Now().Add(-24*time.Hour))
	expiredPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read expired cert: %v", err)
	}

	if _, err := loadOrGenerateCert(certPath, keyPath); err != nil {
		t.Fatalf("loadOrGenerateCert: %v", err)
	}

	regeneratedPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read regenerated cert: %v", err)
	}
	if string(expiredPEM) == string(regeneratedPEM) {
		t.Fatalf("expired cert was reused verbatim instead of regenerated")
	}
	if certExpired(certPath) {
		t.Fatalf("regenerated cert reports as already expired")
	}
}

// TestLoadOrGenerateCert_KeepsValidCert confirms the fix didn't overcorrect
// into regenerating on every call -- a still-valid cert must be reused
// verbatim (matters for serverCertificateHashes pinning: the hash the client
// already fetched must still match on the next request).
func TestLoadOrGenerateCert_KeepsValidCert(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeCertWithExpiry(t, certPath, keyPath, time.Now().Add(13*24*time.Hour))
	validPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read valid cert: %v", err)
	}

	if _, err := loadOrGenerateCert(certPath, keyPath); err != nil {
		t.Fatalf("loadOrGenerateCert: %v", err)
	}

	unchangedPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert after load: %v", err)
	}
	if string(validPEM) != string(unchangedPEM) {
		t.Fatalf("still-valid cert was regenerated, expected reuse")
	}
}
