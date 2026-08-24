// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package hostset

import (
	"reflect"
	"sync"
	"testing"
)

func TestValidHostname(t *testing.T) {
	valid := []string{"localhost", "bloud.local", "example.com", "bloud.example.com", "my-host.example.co.uk", "a"}
	for _, h := range valid {
		if !ValidHostname(h) {
			t.Errorf("ValidHostname(%q) = false, want true", h)
		}
	}
	invalid := []string{"", "EXAMPLE.com", "-lead.com", "trail-.com", "bad host.com", "a..b.com", "localhost:8080"}
	for _, h := range invalid {
		if ValidHostname(h) {
			t.Errorf("ValidHostname(%q) = true, want false", h)
		}
	}
}

func TestNewOrdersPrimaryFirst(t *testing.T) {
	hs := New([]string{"bloud.local", "localhost", "example.com"}, "example.com")
	want := []string{"example.com", "bloud.local", "localhost"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
	if hs.Primary() != "example.com" {
		t.Errorf("Primary() = %q, want example.com", hs.Primary())
	}
}

func TestNewDropsInvalidAndDedupes(t *testing.T) {
	hs := New([]string{"Example.com", "example.com", "bad host", "localhost"}, "")
	want := []string{"localhost", "example.com"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
	if hs.Primary() != "localhost" {
		t.Errorf("Primary() = %q, want localhost fallback", hs.Primary())
	}
}

func TestNewFallsBackToDefaultPrimary(t *testing.T) {
	hs := New([]string{"example.com"}, "not in list")
	if hs.Primary() != "localhost" {
		t.Fatalf("Primary() = %q, want localhost", hs.Primary())
	}
	want := []string{"localhost", "example.com"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
}

func TestBaseURLs(t *testing.T) {
	hs := New([]string{"localhost", "bloud.local", "example.com"}, "localhost")
	urls := hs.BaseURLs()
	want := []string{"http://localhost:8080", "http://bloud.local", "http://example.com"}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("BaseURLs() = %v, want %v", urls, want)
	}
	if hs.PrimaryBaseURL() != "http://localhost:8080" {
		t.Errorf("PrimaryBaseURL() = %q", hs.PrimaryBaseURL())
	}
}

func TestIssuerURLs(t *testing.T) {
	// localhost primary: classic sso.localhost convention.
	hs := New([]string{"localhost", "bloud.local"}, "localhost")
	if got := hs.IssuerBaseURL(); got != "http://sso.localhost:8080" {
		t.Errorf("IssuerBaseURL() = %q, want http://sso.localhost:8080", got)
	}
	if got := hs.IssuerExtraHost(); got != "sso.localhost:host-gateway" {
		t.Errorf("IssuerExtraHost() = %q", got)
	}

	// Custom primary: issuer is the primary host itself on port 80.
	hs = New([]string{"localhost", "bloud.local", "example.com"}, "example.com")
	if got := hs.IssuerBaseURL(); got != "http://example.com" {
		t.Errorf("IssuerBaseURL() = %q, want http://example.com", got)
	}
	if got := hs.IssuerExtraHost(); got != "example.com:host-gateway" {
		t.Errorf("IssuerExtraHost() = %q, want example.com:host-gateway", got)
	}
}

func TestResolveDefaults(t *testing.T) {
	hs, err := Resolve(Input{})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"localhost", "bloud.local"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
	if hs.Primary() != "localhost" {
		t.Errorf("Primary() = %q", hs.Primary())
	}
}

func TestResolveEnvBaseDomain(t *testing.T) {
	hs, err := Resolve(Input{BaseDomain: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if hs.Primary() != "example.com" {
		t.Fatalf("Primary() = %q, want example.com", hs.Primary())
	}
	want := []string{"example.com", "localhost", "bloud.local"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
}

func TestResolveEnvSSOBaseURLOverridesPrimary(t *testing.T) {
	hs, err := Resolve(Input{SSOBaseURL: "http://bloud.example.com:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if hs.Primary() != "bloud.example.com" {
		t.Fatalf("Primary() = %q, want bloud.example.com", hs.Primary())
	}
	if got := hs.PrimaryBaseURL(); got != "http://bloud.example.com:8080" {
		t.Errorf("PrimaryBaseURL() = %q, want http://bloud.example.com:8080 (env verbatim)", got)
	}
	if got := hs.IssuerBaseURL(); got != "http://bloud.example.com:8080" {
		t.Errorf("IssuerBaseURL() = %q, want http://bloud.example.com:8080", got)
	}
}

func TestResolveStoredHostsWinOverEnv(t *testing.T) {
	in := Input{
		Stored: []StoredHost{
			{Hostname: "example.com", Primary: true},
			{Hostname: "other.example.com"},
		},
		BaseDomain: "ignored.example.com",
		SSOBaseURL: "http://ignored:8080",
	}
	hs, err := Resolve(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"example.com", "localhost", "bloud.local", "other.example.com"}
	if got := hs.Hosts(); !reflect.DeepEqual(got, want) {
		t.Errorf("Hosts() = %v, want %v", got, want)
	}
	if hs.Primary() != "example.com" {
		t.Errorf("Primary() = %q", hs.Primary())
	}
}

func TestResolveSkipsCorruptStoredRows(t *testing.T) {
	hs, err := Resolve(Input{Stored: []StoredHost{{Hostname: "bad host"}, {Hostname: "ok.example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !hs.Contains("ok.example.com") {
		t.Error("expected ok.example.com in set")
	}
	if hs.Contains("bad host") {
		t.Error("corrupt row should be skipped")
	}
}

func TestStateConcurrency(t *testing.T) {
	s := NewState(New(BuiltinHosts, DefaultPrimary))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = s.Get() }()
		go func() { defer wg.Done(); s.Set(New(BuiltinHosts, "bloud.local")) }()
	}
	wg.Wait()
	if s.Get().Hosts() == nil {
		t.Fatal("state lost")
	}
}

func TestIsBuiltin(t *testing.T) {
	hs := New(BuiltinHosts, DefaultPrimary)
	if !hs.IsBuiltin("localhost") || !hs.IsBuiltin("bloud.local") {
		t.Error("built-ins not recognized")
	}
	if hs.IsBuiltin("example.com") {
		t.Error("custom host flagged as builtin")
	}
}
