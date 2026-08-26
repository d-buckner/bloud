// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package mdns

import (
	"net"
	"reflect"
	"testing"

	"github.com/miekg/dns"
)

func TestIsLocal(t *testing.T) {
	cases := map[string]bool{
		"bloud.local":          true,
		"jellyfin.bloud.local": true,
		"BLLOUD.LOCAL":         true,
		"localhost":            false,
		"notlocal":             false,
		"example.com":          false,
		"":                     false,
	}
	for host, want := range cases {
		if got := IsLocal(host); got != want {
			t.Errorf("IsLocal(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestDesiredNames(t *testing.T) {
	// localhost is not .local, so only bloud.local's names appear.
	got := DesiredNames([]string{"localhost", "bloud.local"}, []string{"jellyfin", "immich"})
	want := []string{"bloud.local", "immich.bloud.local", "jellyfin.bloud.local"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DesiredNames() = %v, want %v", got, want)
	}
}

func TestDesiredNamesMultipleLocalHosts(t *testing.T) {
	got := DesiredNames([]string{"bloud.local", "other.local", "example.com"}, []string{"jellyfin"})
	want := []string{
		"bloud.local", "jellyfin.bloud.local",
		"other.local", "jellyfin.other.local",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DesiredNames() = %v, want %v", got, want)
	}
}

func TestDesiredNamesNoLocalHosts(t *testing.T) {
	if got := DesiredNames([]string{"localhost", "example.com"}, []string{"jellyfin"}); len(got) != 0 {
		t.Errorf("DesiredNames() = %v, want empty", got)
	}
}

func TestDesiredNamesDeterministicDespiteUnsortedApps(t *testing.T) {
	a := DesiredNames([]string{"bloud.local"}, []string{"zeta", "alpha"})
	b := DesiredNames([]string{"bloud.local"}, []string{"alpha", "zeta"})
	if !reflect.DeepEqual(a, b) {
		t.Errorf("DesiredNames() not deterministic: %v vs %v", a, b)
	}
	want := []string{"bloud.local", "alpha.bloud.local", "zeta.bloud.local"}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("DesiredNames() = %v, want %v", a, want)
	}
}

func TestARecord(t *testing.T) {
	ip := net.ParseIP("192.168.1.10")
	rr := aRecord("bloud.local", ip)

	if rr.Hdr.Name != "bloud.local." {
		t.Errorf("Name = %q, want %q", rr.Hdr.Name, "bloud.local.")
	}
	if rr.Hdr.Rrtype != dns.TypeA {
		t.Errorf("Rrtype = %d, want %d", rr.Hdr.Rrtype, dns.TypeA)
	}
	if rr.Hdr.Ttl != recordTTL {
		t.Errorf("Ttl = %d, want %d", rr.Hdr.Ttl, recordTTL)
	}
	if rr.Hdr.Class&cacheFlush == 0 {
		t.Error("cache-flush bit not set")
	}
	if !rr.A.Equal(ip) {
		t.Errorf("A = %v, want %v", rr.A, ip)
	}
}

func TestRecordsMsgAnnounce(t *testing.T) {
	rrs := []dns.RR{aRecord("bloud.local", net.ParseIP("192.168.1.10"))}
	m := recordsMsg(rrs, false)

	if m.Id != 0 {
		t.Errorf("Id = %d, want 0 (unsolicited)", m.Id)
	}
	if !m.Authoritative {
		t.Error("Authoritative = false, want true")
	}
	if len(m.Answer) != 1 {
		t.Fatalf("len(Answer) = %d, want 1", len(m.Answer))
	}
	if m.Answer[0].Header().Ttl != recordTTL {
		t.Errorf("Ttl = %d, want %d", m.Answer[0].Header().Ttl, recordTTL)
	}
}

func TestRecordsMsgGoodbye(t *testing.T) {
	rrs := []dns.RR{
		aRecord("bloud.local", net.ParseIP("192.168.1.10")),
		aRecord("jellyfin.bloud.local", net.ParseIP("192.168.1.10")),
	}
	m := recordsMsg(rrs, true)

	for _, rr := range m.Answer {
		if rr.Header().Ttl != 0 {
			t.Errorf("%s: Ttl = %d, want 0 (goodbye)", rr.Header().Name, rr.Header().Ttl)
		}
	}
}

func TestAnswerMsg(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn("bloud.local"), dns.TypeA)
	query.Id = 42 // SetQuestion randomizes Id, so set it afterwards

	resp := answerMsg(query, []dns.RR{aRecord("bloud.local", net.ParseIP("192.168.1.10"))})

	if resp.Id != query.Id {
		t.Errorf("Id = %d, want %d (matched query)", resp.Id, query.Id)
	}
	if len(resp.Question) != 0 {
		t.Errorf("len(Question) = %d, want 0 (RFC 6762 §6.2)", len(resp.Question))
	}
	if !resp.Authoritative {
		t.Error("Authoritative = false, want true")
	}
	if len(resp.Answer) != 1 {
		t.Fatalf("len(Answer) = %d, want 1", len(resp.Answer))
	}
}

func TestHasUnicastFlag(t *testing.T) {
	plain := new(dns.Msg)
	plain.SetQuestion(dns.Fqdn("bloud.local"), dns.TypeA)
	if hasUnicastFlag(plain) {
		t.Error("plain query: hasUnicastFlag = true, want false")
	}

	unicast := new(dns.Msg)
	unicast.SetQuestion(dns.Fqdn("bloud.local"), dns.TypeA)
	unicast.Question[0].Qclass = dns.ClassINET | 0x8000
	if !hasUnicastFlag(unicast) {
		t.Error("unicast query: hasUnicastFlag = false, want true")
	}
}
