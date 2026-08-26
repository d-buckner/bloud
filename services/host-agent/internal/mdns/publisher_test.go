// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package mdns

import (
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/ipv4"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/netutil"
)

// ---- fake transport ----

type fakeUnicast struct {
	to  net.Addr
	msg *dns.Msg
}

type fakeSocket struct {
	mu         sync.Mutex
	multicasts []*dns.Msg
	unicasts   []fakeUnicast
	closed     bool
}

func (f *fakeSocket) Multicast(msg *dns.Msg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.multicasts = append(f.multicasts, msg)
	return nil
}

func (f *fakeSocket) Unicast(to net.Addr, msg *dns.Msg) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unicasts = append(f.unicasts, fakeUnicast{to: to, msg: msg})
	return nil
}

func (f *fakeSocket) Read() (*dns.Msg, net.Addr, error) {
	return nil, nil, fmt.Errorf("closed")
}

func (f *fakeSocket) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeSocket) multicastCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.multicasts)
}

func (f *fakeSocket) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func (f *fakeSocket) unicastCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.unicasts)
}

// aNames returns the sorted names of the A records in msg.
func aNames(msg *dns.Msg) []string {
	var names []string
	for _, rr := range msg.Answer {
		if a, ok := rr.(*dns.A); ok {
			names = append(names, a.Hdr.Name[:len(a.Hdr.Name)-1]) // strip trailing dot
		}
	}
	sort.Strings(names)
	return names
}

// aIPs returns the distinct IPs of the A records in msg, sorted.
func aIPs(msg *dns.Msg) []string {
	seen := map[string]struct{}{}
	var ips []string
	for _, rr := range msg.Answer {
		if a, ok := rr.(*dns.A); ok {
			s := a.A.String()
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			ips = append(ips, s)
		}
	}
	sort.Strings(ips)
	return ips
}

// ---- test environment ----

type testEnv struct {
	p         *Publisher
	sockets   []*fakeSocket
	hostState *hostset.State
	apps      []string
	ip        string
	fail      bool // socket factory returns an error
}

func newTestEnv(t *testing.T, hosts, apps []string, ip string) *testEnv {
	t.Helper()
	env := &testEnv{apps: apps, ip: ip, hostState: hostset.NewState(hostset.New(hosts, hosts[0]))}
	env.p = New(Options{
		Logger: slog.Default(),
		Hosts:  env.hostState,
		Apps:   func() []string { return env.apps },
		IP:     func() string { return env.ip },
		// Tests assert on announcement behaviour explicitly; never let the
		// real slirp detection of the test host decide it.
		MulticastAnnounce: func() bool { return true },
		NewSocket: func(_ net.IP) (Socket, error) {
			if env.fail {
				return nil, fmt.Errorf("injected socket failure")
			}
			s := &fakeSocket{}
			env.sockets = append(env.sockets, s)
			return s, nil
		},
	})
	return env
}

// setHosts swaps the live host set, mirroring the orchestrator's
// hostset.State.Set on a hosts change.
func (e *testEnv) setHosts(hosts []string) {
	e.hostState.Set(hostset.New(hosts, hosts[0]))
}

// lastAnnounceMsg returns the last multicast message carrying a live (TTL>0)
// record, skipping goodbyes.
func lastAnnounceMsg(t *testing.T, s *fakeSocket) *dns.Msg {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.multicasts) - 1; i >= 0; i-- {
		if len(s.multicasts[i].Answer) > 0 && s.multicasts[i].Answer[0].Header().Ttl > 0 {
			return s.multicasts[i]
		}
	}
	t.Fatal("no announcement found")
	return nil
}

// lastGoodbyeNames returns the names withdrawn by the most recent TTL-0
// message, or nil when none was sent.
func lastGoodbyeNames(t *testing.T, s *fakeSocket) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.multicasts) - 1; i >= 0; i-- {
		m := s.multicasts[i]
		if len(m.Answer) > 0 && m.Answer[0].Header().Ttl == 0 {
			return aNames(m)
		}
	}
	return nil
}

// ---- tests ----

func TestReconcileAnnouncesHostAndAppSubdomains(t *testing.T) {
	env := newTestEnv(t, []string{"localhost", "bloud.local"}, []string{"jellyfin", "immich"}, "192.168.1.10")

	env.p.Reconcile()

	if len(env.sockets) != 1 {
		t.Fatalf("sockets created = %d, want 1", len(env.sockets))
	}
	s := env.sockets[0]
	if got := s.multicastCount(); got != 2 {
		t.Fatalf("multicasts = %d, want 2 (RFC 6762 §8.3.1 burst)", got)
	}
	msg := lastAnnounceMsg(t, s)
	want := []string{"bloud.local", "immich.bloud.local", "jellyfin.bloud.local"}
	if got := aNames(msg); !equalStrings(got, want) {
		t.Errorf("announced = %v, want %v", got, want)
	}
	if got := aIPs(msg); !equalStrings(got, []string{"192.168.1.10"}) {
		t.Errorf("record IPs = %v, want [192.168.1.10]", got)
	}

	// Second reconcile with unchanged state: no new traffic.
	env.p.Reconcile()
	if got := s.multicastCount(); got != 2 {
		t.Errorf("multicasts after no-op reconcile = %d, want 2", got)
	}
}

func TestReconcileAppRemovedSendsGoodbye(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, []string{"jellyfin", "immich"}, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]

	env.apps = []string{"jellyfin"}
	env.p.Reconcile()

	want := []string{"immich.bloud.local"}
	if got := lastGoodbyeNames(t, s); !equalStrings(got, want) {
		t.Errorf("goodbye names = %v, want %v", got, want)
	}
	// Surviving names stay put: only the goodbye goes out, no re-announce.
	if got := s.multicastCount(); got != 3 {
		t.Errorf("multicasts = %d, want 3 (2 burst + 1 goodbye)", got)
	}
}

func TestReconcileHostRemovedSendsGoodbyes(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local", "other.local"}, []string{"jellyfin"}, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]

	env.setHosts([]string{"bloud.local"})
	env.p.Reconcile()

	want := []string{"jellyfin.other.local", "other.local"}
	if got := lastGoodbyeNames(t, s); !equalStrings(got, want) {
		t.Errorf("goodbye names = %v, want %v", got, want)
	}
}

func TestReconcileNoIPThenIPAppears(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, nil, "")
	env.p.Reconcile()
	if len(env.sockets) != 0 {
		t.Fatalf("sockets created without an IP = %d, want 0", len(env.sockets))
	}

	env.ip = "192.168.1.10"
	env.p.Reconcile()
	if len(env.sockets) != 1 {
		t.Fatalf("sockets after IP appeared = %d, want 1", len(env.sockets))
	}
	if got := aNames(lastAnnounceMsg(t, env.sockets[0])); len(got) != 1 || got[0] != "bloud.local" {
		t.Errorf("announced = %v, want [bloud.local]", got)
	}
}

func TestReconcileIPChangeRetargetsAllRecords(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, []string{"jellyfin"}, "192.168.1.10")
	env.p.Reconcile()
	old := env.sockets[0]

	env.ip = "192.168.1.11"
	env.p.Reconcile()

	// Old socket: goodbye at the old IP, then closed.
	if got := lastGoodbyeNames(t, old); len(got) != 2 {
		t.Errorf("goodbye names = %v, want 2 names", got)
	}
	if !old.isClosed() {
		t.Error("old socket not closed after IP change")
	}
	// New socket: full re-announcement at the new IP.
	if len(env.sockets) != 2 {
		t.Fatalf("sockets = %d, want 2", len(env.sockets))
	}
	msg := lastAnnounceMsg(t, env.sockets[1])
	if got := aIPs(msg); !equalStrings(got, []string{"192.168.1.11"}) {
		t.Errorf("new record IPs = %v, want [192.168.1.11]", got)
	}
}

func TestReconcileSocketFailureRetries(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, nil, "192.168.1.10")
	env.fail = true
	env.p.Reconcile()
	if len(env.sockets) != 0 {
		t.Fatalf("sockets created despite factory failure = %d", len(env.sockets))
	}

	env.fail = false
	env.p.Reconcile()
	if len(env.sockets) != 1 {
		t.Fatalf("sockets after recovery = %d, want 1", len(env.sockets))
	}
}

func TestHandleQueryAnswersOnlyAdvertisedNames(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, []string{"jellyfin"}, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]

	// Multicast query for an advertised name: answered via multicast.
	fromMC := &net.UDPAddr{IP: net.ParseIP("224.0.0.251"), Port: 5353}
	hit := new(dns.Msg)
	hit.SetQuestion(dns.Fqdn("bloud.local"), dns.TypeA)
	hit.Id = 1 // SetQuestion randomizes Id, so set it afterwards
	if err := env.p.handleQuery(s, hit, fromMC); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}
	if got := s.multicastCount(); got != 3 { // 2 burst + 1 answer
		t.Fatalf("multicasts = %d, want 3", got)
	}
	resp := s.multicasts[2]
	if len(resp.Answer) != 1 {
		t.Fatalf("answer RRs = %d, want 1", len(resp.Answer))
	}
	if a := resp.Answer[0].(*dns.A); !a.A.Equal(net.ParseIP("192.168.1.10")) {
		t.Errorf("A = %v, want 192.168.1.10", a.A)
	}
	if resp.Id != hit.Id {
		t.Errorf("reply Id = %d, want %d", resp.Id, hit.Id)
	}

	// Non-advertised name: no response.
	miss := new(dns.Msg)
	miss.Id = 2
	miss.SetQuestion(dns.Fqdn("unknown.bloud.local"), dns.TypeA)
	if err := env.p.handleQuery(s, miss, fromMC); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}
	if got := s.multicastCount(); got != 3 {
		t.Errorf("multicasts after miss = %d, want 3", got)
	}
}

func TestHandleQueryHonoursUnicastFlag(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, nil, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]
	before := s.multicastCount()

	query := new(dns.Msg)
	query.Id = 7
	query.SetQuestion(dns.Fqdn("bloud.local"), dns.TypeA)
	query.Question[0].Qclass = dns.ClassINET | 0x8000
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.99"), Port: 50002}
	if err := env.p.handleQuery(s, query, from); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}

	if got := s.multicastCount(); got != before {
		t.Errorf("multicasts = %d, want %d (reply must be unicast)", got, before)
	}
	if got := s.unicastCount(); got != 1 {
		t.Fatalf("unicasts = %d, want 1", got)
	}
	if got := s.unicasts[0].to; got.String() != from.String() {
		t.Errorf("unicast to %v, want %v", got, from)
	}
}

// TestHandleQueryAnswersUnicastQueryViaUnicast verifies RFC 6762 §6.7: a
// query arriving from a unicast address (no QU bit) must be answered with a
// unicast response. This is the path a host uses to reach the announcer
// through a forwarded UDP 5353 in a dev VM — a multicast reply would never
// make it back to the sender.
func TestHandleQueryAnswersUnicastQueryViaUnicast(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, []string{"jellyfin"}, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]
	beforeMulticast := s.multicastCount()
	beforeUnicast := s.unicastCount()

	query := new(dns.Msg)
	query.Id = 9
	query.SetQuestion(dns.Fqdn("jellyfin.bloud.local"), dns.TypeA)
	from := &net.UDPAddr{IP: net.ParseIP("192.168.1.50"), Port: 50003}
	if err := env.p.handleQuery(s, query, from); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}

	if got := s.multicastCount(); got != beforeMulticast {
		t.Errorf("multicasts = %d, want %d (reply must not be multicast)", got, beforeMulticast)
	}
	if got := s.unicastCount(); got != beforeUnicast+1 {
		t.Fatalf("unicasts = %d, want %d", got, beforeUnicast+1)
	}
	resp := s.unicasts[beforeUnicast]
	if resp.to.String() != from.String() {
		t.Errorf("unicast to %v, want %v", resp.to, from)
	}
	if len(resp.msg.Answer) != 1 {
		t.Fatalf("answer RRs = %d, want 1", len(resp.msg.Answer))
	}
	if a := resp.msg.Answer[0].(*dns.A); a.Hdr.Name != "jellyfin.bloud.local." {
		t.Errorf("answer name = %v, want jellyfin.bloud.local.", a.Hdr.Name)
	}
}

// TestReconcileSkipsMulticastWhenAnnounceDisabled simulates the QEMU slirp
// dev VM: multicast cannot reach the LAN, and the guest's own port-5353
// multicast traffic corrupts slirp's hostfwd state, breaking the unicast
// query path. The publisher must track the names and answer unicast queries,
// but send no multicast at all — no announcements, no re-announcements, no
// TTL-0 goodbyes.
func TestReconcileSkipsMulticastWhenAnnounceDisabled(t *testing.T) {
	var s *fakeSocket
	appList := []string{"jellyfin"}
	hostState := hostset.NewState(hostset.New([]string{"bloud.local"}, "bloud.local"))
	p := New(Options{
		Logger:            slog.Default(),
		Hosts:             hostState,
		Apps:              func() []string { return appList },
		IP:                func() string { return "10.0.2.15" },
		MulticastAnnounce: func() bool { return false },
		NewSocket: func(_ net.IP) (Socket, error) {
			s = &fakeSocket{}
			return s, nil
		},
	})

	p.Reconcile()
	if got := s.multicastCount(); got != 0 {
		t.Fatalf("multicasts after Reconcile = %d, want 0 (slirp: no announcements)", got)
	}

	// Unicast queries must still be answered.
	query := new(dns.Msg)
	query.Id = 7
	query.SetQuestion(dns.Fqdn("jellyfin.bloud.local"), dns.TypeA)
	from := &net.UDPAddr{IP: net.ParseIP("10.0.2.2"), Port: 40000}
	if err := p.handleQuery(s, query, from); err != nil {
		t.Fatalf("handleQuery: %v", err)
	}
	if got := s.unicastCount(); got != 1 {
		t.Errorf("unicasts = %d, want 1", got)
	}
	if got := s.multicastCount(); got != 0 {
		t.Errorf("multicasts after query = %d, want 0", got)
	}

	// Name removal and Close must not emit TTL-0 goodbyes either.
	appList = nil
	p.Reconcile()
	p.Close()
	if got := s.multicastCount(); got != 0 {
		t.Errorf("multicasts after removal+Close = %d, want 0 (slirp: no goodbyes)", got)
	}
	if !s.isClosed() {
		t.Error("socket not closed")
	}
}

func TestCloseSendsGoodbyes(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, []string{"jellyfin"}, "192.168.1.10")
	env.p.Reconcile()
	s := env.sockets[0]

	env.p.Close()

	if got := lastGoodbyeNames(t, s); len(got) != 2 {
		t.Errorf("goodbye names = %v, want 2 names", got)
	}
	if !s.isClosed() {
		t.Error("socket not closed")
	}
	// Idempotent.
	env.p.Close()
}

func TestCloseWithNoSocket(t *testing.T) {
	env := newTestEnv(t, []string{"bloud.local"}, nil, "")
	env.p.Close() // must not panic
}

func equalStrings(a, b []string) bool {
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

// ---- live round trip ----

// TestLiveMDSNRoundTrip verifies the full wire path on the test machine: a
// real mDNS socket announces a name, a second real socket queries the
// multicast group, and the A answer comes back. Skipped where the machine
// has no usable multicast interface or the mDNS port is taken.
func TestLiveMDSNRoundTrip(t *testing.T) {
	ip := netutil.GetPrimaryIP()
	if ip == "" {
		t.Skip("no non-loopback IPv4 address")
	}
	iface, err := interfaceForIP(net.ParseIP(ip))
	if err != nil {
		t.Skipf("no interface for %s: %v", ip, err)
	}

	name := fmt.Sprintf("bloud-mdns-test-%d", time.Now().UnixNano())
	p := New(Options{
		Logger: slog.Default(),
		Hosts:  hostset.NewState(hostset.New([]string{name + "." + localDomain}, name+"."+localDomain)),
		Apps:   func() []string { return nil },
		IP:     func() string { return ip },
		NewSocket: func(_ net.IP) (Socket, error) {
			return newMDSNSocket(iface)
		},
	})

	p.Reconcile()
	defer p.Close()
	if len(p.live) == 0 {
		t.Skip("nothing was advertised (mDNS port busy or interface unavailable)")
	}

	// Query the multicast group from a second real socket.
	client, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		t.Skipf("client socket: %v", err)
	}
	defer client.Close()
	pc := ipv4.NewPacketConn(client)
	if err := pc.JoinGroup(iface, mdnsGroupAddr); err != nil {
		t.Skipf("join multicast group: %v", err)
	}

	query := new(dns.Msg)
	query.Id = 99
	query.SetQuestion(dns.Fqdn(name+"."+localDomain), dns.TypeA)
	qbuf, _ := query.Pack()
	if _, err := client.WriteToUDP(qbuf, mdnsGroupAddr); err != nil {
		t.Fatalf("send query: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	client.SetReadDeadline(deadline)
	buf := make([]byte, maxQuerySize)
	for {
		n, _, err := client.ReadFromUDP(buf)
		if err != nil {
			t.Skipf("no mDNS reply within 5s (multicast not usable here): %v", err)
		}
		resp := new(dns.Msg)
		if err := resp.Unpack(buf[:n]); err != nil {
			continue
		}
		if resp.Id != query.Id {
			continue
		}
		for _, rr := range resp.Answer {
			if a, ok := rr.(*dns.A); ok && a.Hdr.Name == dns.Fqdn(name+"."+localDomain) && a.A.Equal(net.ParseIP(ip)) {
				return // success
			}
		}
	}
}
