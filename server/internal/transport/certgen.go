package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// GenerateSelfSigned writes a self-signed ECDSA (P-256) cert/key pair to the
// given paths, creating parent directories as needed. It exists so headless dev
// and tests can stand up WebTransport without a CA. The cert is valid for
// localhost / 127.0.0.1 / ::1.
//
// Validity is intentionally short (under two weeks): the W3C
// serverCertificateHashes API the browser uses for self-signed certs (see
// client.ts ClientOptions.serverCertHashes) rejects certs valid for longer.
func GenerateSelfSigned(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("transport: generate key: %w", err)
	}

	now := time.Now().UTC()
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(now.UnixNano()),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(13 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("transport: create certificate: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("transport: marshal key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return fmt.Errorf("transport: mkdir cert dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return fmt.Errorf("transport: mkdir key dir: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("transport: write cert: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("transport: write key: %w", err)
	}
	return nil
}

// CertSHA256 returns the SHA-256 hash of the DER-encoded leaf certificate in
// the PEM file at certPath, both as raw bytes and base64. The browser needs
// this for WebTransport's serverCertificateHashes when accepting a self-signed
// dev cert without a CA (client.ts ClientOptions.serverCertHashes).
func CertSHA256(certPath string) (raw [32]byte, b64 string, err error) {
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return raw, "", fmt.Errorf("transport: read cert: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return raw, "", fmt.Errorf("transport: %s does not contain a PEM certificate", certPath)
	}
	raw = sha256.Sum256(block.Bytes)
	return raw, base64.StdEncoding.EncodeToString(raw[:]), nil
}

// loadOrGenerateCert loads the cert/key pair, generating a self-signed pair
// first if either file is missing. Returns the parsed tls.Certificate.
func loadOrGenerateCert(certPath, keyPath string) (tls.Certificate, error) {
	if !fileExists(certPath) || !fileExists(keyPath) {
		if err := GenerateSelfSigned(certPath, keyPath); err != nil {
			return tls.Certificate{}, err
		}
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
