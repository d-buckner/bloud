package netutil

import (
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

func TestBuildBaseURLsWithPort(t *testing.T) {
	urls := BuildBaseURLs("http://bloud.local:8080")

	if len(urls) == 0 {
		t.Fatal("BuildBaseURLs() returned empty list")
	}
	if urls[0] != "http://bloud.local:8080" {
		t.Errorf("first URL = %q, want %q", urls[0], "http://bloud.local:8080")
	}

	// Additional entries should be http://<ip>:8080
	for _, u := range urls[1:] {
		if !strings.HasPrefix(u, "http://") {
			t.Errorf("URL %q missing http:// prefix", u)
		}
		if !strings.Contains(u, ":8080") {
			t.Errorf("URL %q should contain :8080", u)
		}
	}
}

func TestBuildBaseURLsWithoutPort(t *testing.T) {
	urls := BuildBaseURLs("http://bloud.local")

	if urls[0] != "http://bloud.local" {
		t.Errorf("first URL = %q, want %q", urls[0], "http://bloud.local")
	}

	// When no port is specified, IP URLs should not have a port either
	for _, u := range urls[1:] {
		// Count colons: "http://1.2.3.4" has one colon (in scheme).
		// "http://1.2.3.4:8080" has two.
		if strings.Count(u, ":") > 1 {
			t.Errorf("URL %q should not have a port suffix", u)
		}
	}
}

func TestBuildBaseURLsHTTPS(t *testing.T) {
	urls := BuildBaseURLs("https://bloud.example.com")

	if urls[0] != "https://bloud.example.com" {
		t.Errorf("first URL = %q, want %q", urls[0], "https://bloud.example.com")
	}

	// IP-based URLs should inherit the scheme
	for _, u := range urls[1:] {
		if !strings.HasPrefix(u, "https://") {
			t.Errorf("URL %q should use https scheme", u)
		}
	}
}

func TestBuildBaseURLsInvalidURL(t *testing.T) {
	// Should not panic, just return the configured URL as-is
	urls := BuildBaseURLs("://broken")
	if len(urls) == 0 {
		t.Fatal("BuildBaseURLs() returned empty list for invalid URL")
	}
}
