// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package mdns

import (
	"net"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

// cacheFlush is the Multicast DNS cache-flush bit (RFC 6762 §10.2): tells
// resolvers to discard any cached copy of the record.
const cacheFlush = uint16(1) << 15

// recordTTL is the TTL advertised on every record. Resolvers keep entries for
// twice the TTL (240s), so the publisher re-announces at least every TTL to
// keep caches fresh (RFC 6762 §10.1).
const recordTTL uint32 = 120

// localDomain is the mDNS domain (RFC 6762 reserves the .local TLD for it).
const localDomain = "local"

// IsLocal reports whether host belongs to the .local TLD — the only hostnames
// mDNS can legitimately advertise.
func IsLocal(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), "."+localDomain)
}

// DesiredNames computes the mDNS names to advertise: for every .local host in
// the set (in set order, primary first), the host itself plus one subdomain
// per app — mirroring the domain-agnostic <app>.<host> Traefik routes, so a
// name advertised here is a name Traefik actually serves.
func DesiredNames(hosts, appIDs []string) []string {
	apps := append([]string(nil), appIDs...)
	sort.Strings(apps)

	var names []string
	for _, h := range hosts {
		if !IsLocal(h) {
			continue
		}
		candidates := make([]string, 0, 1+len(apps))
		candidates = append(candidates, h)
		for _, a := range apps {
			candidates = append(candidates, a+"."+h)
		}
		for _, n := range candidates {
			if !containsString(names, n) {
				names = append(names, n)
			}
		}
	}
	return names
}

// aRecord builds the A record for name (a .local hostname, no trailing dot)
// with the cache-flush bit set.
func aRecord(name string, ip net.IP) *dns.A {
	return &dns.A{
		Hdr: dns.RR_Header{
			Name:   dns.Fqdn(name),
			Rrtype: dns.TypeA,
			Class:  dns.ClassINET | cacheFlush,
			Ttl:    recordTTL,
		},
		A: ip,
	}
}

// recordsMsg builds an unsolicited message carrying records (RFC 6762 §8.3).
func recordsMsg(records []dns.RR, goodbye bool) *dns.Msg {
	m := new(dns.Msg)
	m.Id = 0 // RFC 6762 §6.2: unsolicited messages MUST have ID 0
	m.Authoritative = true
	for _, rr := range records {
		if goodbye {
			rr.Header().Ttl = 0
		}
		m.Answer = append(m.Answer, rr)
	}
	return m
}

// answerMsg builds the reply to a query for one of our names.
func answerMsg(query *dns.Msg, records []dns.RR) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(query)
	m.Question = nil // RFC 6762 §6.2: responses MUST NOT contain questions
	m.RecursionDesired = false
	m.Authoritative = true
	m.Answer = records
	return m
}

// hasUnicastFlag reports whether any question sets the "respond via unicast"
// bit (RFC 6762 §6.4).
func hasUnicastFlag(msg *dns.Msg) bool {
	for _, q := range msg.Question {
		if q.Qclass&0x8000 != 0 {
			return true
		}
	}
	return false
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
