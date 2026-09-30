// SPDX-License-Identifier: AGPL-3.0-only

package netutil

import (
	"net"
	"strings"
	"testing"
)

func TestDetectLocalIPs(t *testing.T) {
	ips := DetectLocalIPs()

	// Should return at least one IP on any machine with a network interface
	if len(ips) == 0 {
		t.Skip("no non-loopback IPv4 addresses found (CI environment?)")
	}

	for _, ip := range ips {
		if ip == "127.0.0.1" {
			t.Errorf("DetectLocalIPs() returned loopback address")
		}
		if strings.Contains(ip, ":") {
			t.Errorf("DetectLocalIPs() returned non-IPv4 address: %s", ip)
		}
	}
}

func TestLANBaseURLsNonDefaultPort(t *testing.T) {
	urls := LANBaseURLs(8080)
	if len(urls) == 0 {
		t.Skip("no non-loopback IPv4 addresses found (CI environment?)")
	}

	for _, u := range urls {
		if !strings.HasPrefix(u, "http://") {
			t.Errorf("URL %q missing http:// prefix", u)
		}
		host := strings.TrimPrefix(u, "http://")
		h, port, err := net.SplitHostPort(host)
		if err != nil {
			t.Fatalf("URL %q should carry an explicit port: %v", u, err)
		}
		if port != "8080" {
			t.Errorf("URL %q port = %q, want 8080", u, port)
		}
		if net.ParseIP(h) == nil {
			t.Errorf("URL %q host %q is not an IP address", u, h)
		}
	}
}

// The http default port is left off the URL: http://10.0.0.5 and
// http://10.0.0.5:80 are the same origin, and the shorter form is what a
// redirect should carry.
func TestLANBaseURLsDefaultPortOmitted(t *testing.T) {
	for _, port := range []int{80, 0} {
		urls := LANBaseURLs(port)
		if len(urls) == 0 {
			t.Skip("no non-loopback IPv4 addresses found (CI environment?)")
		}
		for _, u := range urls {
			if strings.Count(u, ":") > 1 {
				t.Errorf("LANBaseURLs(%d): URL %q should not carry a port suffix", port, u)
			}
		}
	}
}

// The scheme of a LAN IP URL is never https, whatever the deployment's public
// scheme is. This is the regression that made http://10.0.0.210:8080 redirect
// its login to https://10.0.0.210: a bare address has no TLS terminator in
// front of it and no certificate, so an https IP URL is unreachable by
// construction.
func TestLANBaseURLsNeverHTTPS(t *testing.T) {
	for _, port := range []int{0, 80, 443, 8080, 8443} {
		for _, u := range LANBaseURLs(port) {
			if strings.HasPrefix(u, "https://") {
				t.Errorf("LANBaseURLs(%d) produced %q; a LAN IP URL is always plain http", port, u)
			}
		}
	}
}

func TestLANBaseURLsEmptyWithoutInterfaces(t *testing.T) {
	// Nothing to assert about content, only that the shape holds: no entry is
	// ever emitted for a host that was not detected.
	detected := map[string]bool{}
	for _, ip := range DetectLocalIPs() {
		detected[ip] = true
	}
	for _, u := range LANBaseURLs(8080) {
		host, _, err := net.SplitHostPort(strings.TrimPrefix(u, "http://"))
		if err != nil {
			host = strings.TrimPrefix(u, "http://")
		}
		if !detected[host] {
			t.Errorf("LANBaseURLs emitted %q for an address that was not detected", u)
		}
	}
}
