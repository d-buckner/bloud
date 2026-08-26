// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package tlsca generates and manages Bloud's local certificate authority and
// leaf certificate.
//
// The CA is created once and never regenerated: browser trust pins the CA
// fingerprint, so stability matters more than expiry. Rotation is a manual,
// documented operation (remove the tls dir and re-bootstrap), not a reconcile
// behavior. A merged trust bundle (system CAs + the Bloud CA) is written for
// containers to consume via SSL_CERT_FILE, so they keep validating public CAs
// too.
//
// The generated files:
//
//	<dataDir>/tls/ca.crt          self-signed CA certificate (0644)
//	<dataDir>/tls/ca.key          CA private key, ECDSA P-256 (0600)
//	<dataDir>/tls/server.crt      leaf certificate signed by the CA (0644)
//	<dataDir>/tls/server.key      leaf private key, ECDSA P-256 (0600)
//	<dataDir>/tls/ca-bundle.crt   system CA bundle + Bloud CA (0644)
package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// caValidity is 10 years — long enough that rotation is a rare manual
// operation in practice.
const caValidity = 10 * 365 * 24 * time.Hour

// leafValidity matches the CA; the leaf is regenerated only if the tls dir is
// reset, so a long validity is safe.
const leafValidity = 10 * 365 * 24 * time.Hour

// Dir returns the TLS directory under dataDir.
func Dir(dataDir string) string { return filepath.Join(dataDir, "tls") }

// Paths returns the canonical TLS file locations under dataDir.
func Paths(dataDir string) (caCert, caKey, leafCert, leafKey, bundle string) {
	d := Dir(dataDir)
	caCert = filepath.Join(d, "ca.crt")
	caKey = filepath.Join(d, "ca.key")
	leafCert = filepath.Join(d, "server.crt")
	leafKey = filepath.Join(d, "server.key")
	bundle = filepath.Join(d, "ca-bundle.crt")
	return caCert, caKey, leafCert, leafKey, bundle
}

// EnsureAll generates the CA + leaf (if missing) and refreshes the trust
// bundle. It is idempotent: existing CA and leaf files are left untouched, so
// the CA fingerprint is stable across restarts. The bundle is rewritten on
// every call so it tracks the system bundle.
func EnsureAll(dataDir, baseDomain string) error {
	tlsDir := Dir(dataDir)
	if err := os.MkdirAll(tlsDir, 0700); err != nil {
		return fmt.Errorf("create tls dir %s: %w", tlsDir, err)
	}
	caCertPath, caKeyPath, leafCertPath, leafKeyPath, bundlePath := Paths(dataDir)

	caCert, caKey, err := loadOrCreateCA(caCertPath, caKeyPath)
	if err != nil {
		return err
	}

	if err := ensureLeaf(leafCertPath, leafKeyPath, caCert, caKey, baseDomain); err != nil {
		return err
	}

	if err := writeBundle(bundlePath, caCertPath); err != nil {
		return err
	}
	return nil
}

// loadOrCreateCA returns the existing CA (cert + key) when both files are
// present and parse, otherwise generates a new one and writes it.
func loadOrCreateCA(caCertPath, caKeyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	caCertPEM, certErr := os.ReadFile(caCertPath)
	caKeyPEM, keyErr := os.ReadFile(caKeyPath)
	if certErr == nil && keyErr == nil {
		cert, err := parseCertPEM(caCertPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("existing CA cert %s is corrupt: %w (remove the tls dir and re-bootstrap)", caCertPath, err)
		}
		key, err := parseECKeyPEM(caKeyPEM)
		if err != nil {
			return nil, nil, fmt.Errorf("existing CA key %s is corrupt: %w (remove the tls dir and re-bootstrap)", caKeyPath, err)
		}
		return cert, key, nil
	}

	cert, key, certDER, keyPEM := generateCA()
	if err := os.WriteFile(caCertPath, pemEncode("CERTIFICATE", certDER), 0644); err != nil {
		return nil, nil, fmt.Errorf("write CA cert: %w", err)
	}
	if err := os.WriteFile(caKeyPath, keyPEM, 0600); err != nil {
		return nil, nil, fmt.Errorf("write CA key: %w", err)
	}
	return cert, key, nil
}

// ensureLeaf returns when a leaf cert + key already exist, otherwise generates
// a new leaf signed by the CA with the given SANs and writes it.
func ensureLeaf(leafCertPath, leafKeyPath string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, baseDomain string) error {
	if _, err := os.Stat(leafCertPath); err == nil {
		if _, keyErr := os.Stat(leafKeyPath); keyErr == nil {
			return nil // leaf already present; keep it for fingerprint stability
		}
	}

	certDER, keyPEM := generateLeaf(caCert, caKey, baseDomain)
	if err := os.WriteFile(leafCertPath, pemEncode("CERTIFICATE", certDER), 0644); err != nil {
		return fmt.Errorf("write leaf cert: %w", err)
	}
	if err := os.WriteFile(leafKeyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("write leaf key: %w", err)
	}
	return nil
}

// writeBundle writes the merged trust bundle: the system CA bundle (when
// found) followed by the Bloud CA certificate.
func writeBundle(bundlePath, caCertPath string) error {
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("read CA cert for bundle: %w", err)
	}

	var bundle []byte
	if sys := findSystemBundle(); sys != "" {
		if sysPEM, rerr := os.ReadFile(sys); rerr == nil {
			bundle = append(bundle, sysPEM...)
			if n := len(bundle); n > 0 && bundle[n-1] != '\n' {
				bundle = append(bundle, '\n')
			}
		}
	}
	bundle = append(bundle, caCertPEM...)
	if n := len(bundle); n > 0 && bundle[n-1] != '\n' {
		bundle = append(bundle, '\n')
	}
	if err := os.WriteFile(bundlePath, bundle, 0644); err != nil {
		return fmt.Errorf("write CA bundle: %w", err)
	}
	return nil
}

// generateCA creates a self-signed ECDSA P-256 CA valid for caValidity.
func generateCA() (cert *x509.Certificate, key *ecdsa.PrivateKey, certDER, keyPEM []byte) {
	key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Bloud Local CA",
			Organization: []string{"Bloud"},
		},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(fmt.Sprintf("tlsca: create CA certificate: %v", err))
	}
	cert, _ = x509.ParseCertificate(certDER)
	keyPEM = pemEncode("EC PRIVATE KEY", mustMarshalECKey(key))
	return cert, key, certDER, keyPEM
}

// generateLeaf creates a server certificate signed by the CA, covering the
// localhost variants, IP loopbacks, and the configured base domain.
func generateLeaf(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, baseDomain string) (certDER, keyPEM []byte) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Bloud Local Server",
			Organization: []string{"Bloud"},
		},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(leafValidity),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    dnsNames(baseDomain),
		IPAddresses: loopbackIPs(),
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		panic(fmt.Sprintf("tlsca: create leaf certificate: %v", err))
	}
	keyPEM = pemEncode("EC PRIVATE KEY", mustMarshalECKey(key))
	return certDER, keyPEM
}

// dnsNames returns the sorted, deduplicated DNS SANs: the localhost variants
// (always), plus the configured base domain, its wildcard, and the literal
// SSO hostname. The SSO hostname must be listed literally: OpenSSL and NSS
// reject wildcard patterns whose suffix is a single label (verified:
// *.localhost does not match sso.localhost), while Go and BoringSSL accept
// them — the literal entry makes the issuer verify in every TLS stack.
// (App subdomains over TLS rely on the *.<baseDomain> wildcard, which is
// accepted by strict stacks whenever the base domain has ≥2 labels.)
func dnsNames(baseDomain string) []string {
	set := map[string]bool{
		"localhost":     true,
		"*.localhost":   true,
		"sso.localhost": true,
	}
	if baseDomain != "" && baseDomain != "localhost" {
		set[baseDomain] = true
		set["*."+baseDomain] = true
		set["sso."+baseDomain] = true
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func loopbackIPs() []net.IP {
	return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
}

// findSystemBundle returns the first existing system CA bundle path, or "" when
// none is found (the bundle then contains only the Bloud CA).
func findSystemBundle() string {
	candidates := []string{
		"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu
		"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL/Fedora
		"/etc/ssl/ca-bundle.pem",             // openSUSE
		"/etc/ssl/cert.pem",                  // macOS, Alpine
		"/usr/local/etc/openssl/cert.pem",    // Homebrew
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func pemEncode(certType string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: certType, Bytes: der})
}

func mustMarshalECKey(key *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic(fmt.Sprintf("tlsca: marshal EC key: %v", err))
	}
	return der
}

func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECKeyPEM(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}
