// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package tlsca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"testing"
)

func TestEnsureAllGeneratesFiles(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureAll(dir, "localhost"); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}

	caCertPath, caKeyPath, leafCertPath, leafKeyPath, bundlePath := Paths(dir)
	for _, p := range []string{caCertPath, caKeyPath, leafCertPath, leafKeyPath, bundlePath} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("missing file %s: %v", p, err)
		}
		if p == caKeyPath || p == leafKeyPath {
			if info.Mode().Perm() != 0600 {
				t.Errorf("%s mode = %o, want 0600", p, info.Mode().Perm())
			}
		}
	}

	caCert, err := parseCertPEM(mustRead(t, caCertPath))
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	if !caCert.IsCA {
		t.Error("CA cert IsCA = false, want true")
	}
	if caCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cert missing KeyUsageCertSign")
	}

	leafCert, err := parseCertPEM(mustRead(t, leafCertPath))
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	if leafCert.Subject.CommonName != "Bloud Local Server" {
		t.Errorf("leaf CN = %q", leafCert.Subject.CommonName)
	}
	for _, want := range []string{"localhost", "*.localhost", "sso.localhost"} {
		if !containsString(leafCert.DNSNames, want) {
			t.Errorf("leaf DNSNames missing %q: %v", want, leafCert.DNSNames)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if !containsIP(leafCert.IPAddresses, ip) {
			t.Errorf("leaf IPAddresses missing %s: %v", ip, leafCert.IPAddresses)
		}
	}

	// The leaf must verify against the CA for an app subdomain.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "appflowy.localhost"}); err != nil {
		t.Errorf("leaf verify for appflowy.localhost: %v", err)
	}
	if _, err := leafCert.Verify(x509.VerifyOptions{Roots: pool, DNSName: "sso.localhost"}); err != nil {
		t.Errorf("leaf verify for sso.localhost: %v", err)
	}

	// The CA key must match the CA cert.
	caKey, err := parseECKeyPEM(mustRead(t, caKeyPath))
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	if caKey.PublicKey.Curve != elliptic.P256() {
		t.Errorf("CA key curve = %v, want P-256", caKey.Curve)
	}
	if caCert.PublicKey.(*ecdsa.PublicKey).X.Cmp(caKey.PublicKey.X) != 0 {
		t.Error("CA key does not match CA cert")
	}
}

func TestEnsureAllIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureAll(dir, "localhost"); err != nil {
		t.Fatalf("first EnsureAll: %v", err)
	}
	caCertPath, caKeyPath, leafCertPath, leafKeyPath, bundlePath := Paths(dir)
	first := map[string][]byte{}
	for name, p := range map[string]string{
		"ca.crt": caCertPath, "ca.key": caKeyPath,
		"server.crt": leafCertPath, "server.key": leafKeyPath,
	} {
		first[name] = mustRead(t, p)
	}

	// Simulate a restart: EnsureAll again, plus a tamper-free second pass.
	if err := EnsureAll(dir, "localhost"); err != nil {
		t.Fatalf("second EnsureAll: %v", err)
	}
	for name, p := range map[string]string{
		"ca.crt": caCertPath, "ca.key": caKeyPath,
		"server.crt": leafCertPath, "server.key": leafKeyPath,
	} {
		if got := mustRead(t, p); !bytesEqual(got, first[name]) {
			t.Errorf("%s changed across EnsureAll calls (must be generated once)", name)
		}
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Errorf("bundle missing after second EnsureAll: %v", err)
	}
}

func TestBaseDomainSANs(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureAll(dir, "bloud.local"); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	_, _, leafCertPath, _, _ := Paths(dir)
	leafCert, err := parseCertPEM(mustRead(t, leafCertPath))
	if err != nil {
		t.Fatalf("parse leaf cert: %v", err)
	}
	for _, want := range []string{"bloud.local", "*.bloud.local", "sso.bloud.local", "localhost", "*.localhost", "sso.localhost"} {
		if !containsString(leafCert.DNSNames, want) {
			t.Errorf("leaf DNSNames missing %q: %v", want, leafCert.DNSNames)
		}
	}
}

func TestBundleContainsCA(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureAll(dir, "localhost"); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	caCertPath, _, _, _, bundlePath := Paths(dir)
	caPEM := mustRead(t, caCertPath)
	bundle := mustRead(t, bundlePath)

	// The bundle must contain a decodable PEM block identical to the CA cert.
	rest := bundle
	found := false
	for len(rest) > 0 {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" && bytesEqual(block.Bytes, pemBlockBytes(t, caPEM)) {
			found = true
			break
		}
	}
	if !found {
		t.Error("bundle does not contain the Bloud CA certificate")
	}
}

func TestCorruptCARejected(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureAll(dir, "localhost"); err != nil {
		t.Fatalf("EnsureAll: %v", err)
	}
	caCertPath, _, _, _, _ := Paths(dir)
	if err := os.WriteFile(caCertPath, []byte("garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := EnsureAll(dir, "localhost"); err == nil {
		t.Error("EnsureAll succeeded on a corrupt CA cert, want error")
	}
}

// --- helpers ---

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pemBlockBytes(t *testing.T, pemBytes []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("no PEM block")
	}
	return block.Bytes
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsIP(ips []net.IP, want string) bool {
	for _, ip := range ips {
		if ip.String() == want {
			return true
		}
	}
	return false
}
