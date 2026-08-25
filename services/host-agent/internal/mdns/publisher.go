// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package mdns advertises the instance's .local hostnames over Multicast DNS
// (RFC 6762) so devices on the local network can reach the dashboard and apps
// as http://bloud.local and http://<app>.bloud.local without any DNS
// configuration.
//
// The publisher owns one UDP socket on port 5353, joined to the mDNS group on
// the interface that carries the host's primary IPv4 address. It answers A
// queries for the advertised names and sends unsolicited announcements on
// registration and at least every recordTTL, so resolver caches (which expire
// at twice the TTL) stay fresh. Records are withdrawn with a TTL-0 "goodbye".
//
// The record set is recomputed on demand from live state — the host set and
// the installed apps — so it always matches what Traefik actually serves:
// every .local host in the host set, plus one <app>.<host> subdomain per
// routable installed app.
package mdns

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

const (
	// defaultInterval is how often the publisher re-checks live state
	// (host set, apps, primary IP).
	defaultInterval = 30 * time.Second
	// burstGap is the delay between the two initial announcements of a new
	// record (RFC 6762 §8.3.1).
	burstGap = 120 * time.Millisecond
)

// Options configures a Publisher.
type Options struct {
	// Logger for lifecycle events; nil uses slog.Default().
	Logger *slog.Logger
	// Hosts is the live host set; only hosts ending in ".local" are
	// advertised. Required.
	Hosts *hostset.State
	// Apps returns the catalog IDs of routable (non-system, port-bearing)
	// installed apps; each becomes an <app>.<host> record per advertised
	// host. Required.
	Apps func() []string
	// IP returns the primary LAN IPv4 to publish, "" when unavailable (e.g.
	// a loopback-only machine). The publisher stays idle until an address
	// appears. Required.
	IP func() string
	// Events, when non-nil, triggers an immediate reconcile on app changes
	// (install/uninstall). The periodic loop covers host and IP changes.
	Events *eventbus.Bus
	// ReconcileInterval between state checks; default 30s.
	ReconcileInterval time.Duration
	// NewSocket builds the mDNS socket for the interface carrying ip.
	// Production binds the mDNS port; tests inject a fake.
	NewSocket func(ip net.IP) (Socket, error)
}

// Publisher reconciles the advertised mDNS record set against live host and
// app state.
type Publisher struct {
	logger    *slog.Logger
	hosts     *hostset.State
	apps      func() []string
	ip        func() string
	events    *eventbus.Bus
	interval  time.Duration
	newSocket func(ip net.IP) (Socket, error)

	mu           sync.Mutex
	socket       Socket
	currentIP    string
	live         map[string]struct{} // advertised names
	lastAnnounce time.Time
}

// New builds a Publisher from opts.
func New(opts Options) *Publisher {
	if opts.ReconcileInterval <= 0 {
		opts.ReconcileInterval = defaultInterval
	}
	if opts.NewSocket == nil {
		opts.NewSocket = newSocketForIP
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Publisher{
		logger:    opts.Logger,
		hosts:     opts.Hosts,
		apps:      opts.Apps,
		ip:        opts.IP,
		events:    opts.Events,
		interval:  opts.ReconcileInterval,
		newSocket: opts.NewSocket,
		live:      map[string]struct{}{},
	}
}

// Start launches the publisher in the background. It returns immediately; the
// publisher runs until ctx is cancelled, at which point it sends goodbyes for
// every advertised name and closes the socket.
func Start(ctx context.Context, opts Options) {
	go New(opts).Run(ctx)
}

// Run blocks until ctx is cancelled, reconciling the advertised record set
// against the live host/app state on startup, on every tick, and on app
// change events.
func (p *Publisher) Run(ctx context.Context) {
	var (
		eventsCh     <-chan eventbus.Event
		cancelEvents func()
	)
	if p.events != nil {
		eventsCh, cancelEvents = p.events.Subscribe()
		defer cancelEvents()
	}

	p.Reconcile()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			p.Close()
			return
		case <-ticker.C:
			p.Reconcile()
			p.refreshIfDue()
		case evt := <-eventsCh:
			if evt.Type == eventbus.TypeAppsChanged {
				p.Reconcile()
			}
		}
	}
}

// Reconcile aligns the advertised records with the current host set and
// installed apps: it opens the socket when the primary IP (re)appears,
// announces new names, and sends goodbyes for removed ones. Safe to call
// concurrently.
func (p *Publisher) Reconcile() {
	ip := p.ip()
	names := DesiredNames(p.hosts.Get().Hosts(), p.apps())

	p.mu.Lock()

	// No LAN address: withdraw everything and release the socket.
	if ip == "" {
		if p.socket == nil {
			p.mu.Unlock()
			return
		}
		socket, oldIP := p.socket, p.currentIP
		goodbyes := p.liveNamesLocked()
		p.socket, p.currentIP, p.live = nil, "", map[string]struct{}{}
		p.mu.Unlock()
		p.sendGoodbyes(socket, oldIP, goodbyes)
		_ = socket.Close()
		p.logger.Info("mdns: stopped advertising (no LAN address)")
		return
	}

	// (Re)open the socket when there is none or the primary IP moved. The
	// old socket's records go out with a goodbye on the old address before
	// the new set is announced.
	var (
		oldSocket Socket
		oldIP     string
		oldLive   []string
		reopening bool
	)
	if p.socket == nil || p.currentIP != ip {
		oldSocket, oldIP, oldLive = p.socket, p.currentIP, p.liveNamesLocked()
		reopening = true
	}
	if reopening {
		socket, err := p.newSocket(net.ParseIP(ip))
		if err != nil {
			// Keep the previous socket (if any) answering queries; the next
			// tick retries the switch.
			p.mu.Unlock()
			p.logger.Warn("mdns: socket unavailable, retrying", "ip", ip, "error", err)
			return
		}
		p.socket, p.currentIP, p.live = socket, ip, map[string]struct{}{}
		p.startReader(socket)
	}

	// Diff the desired names against what is already advertised.
	var announcements, goodbyes []string
	for _, n := range names {
		if _, ok := p.live[n]; !ok {
			announcements = append(announcements, n)
		}
	}
	if !reopening {
		for n := range p.live {
			if !containsString(names, n) {
				goodbyes = append(goodbyes, n)
				delete(p.live, n)
			}
		}
	}
	for _, n := range announcements {
		p.live[n] = struct{}{}
	}
	socket := p.socket
	p.mu.Unlock()

	if reopening && oldSocket != nil {
		p.sendGoodbyes(oldSocket, oldIP, oldLive)
		_ = oldSocket.Close()
	}
	if len(goodbyes) > 0 {
		p.sendGoodbyes(socket, ip, goodbyes)
	}
	if len(announcements) > 0 {
		p.announce(socket, ip, announcements)
		p.mu.Lock()
		p.lastAnnounce = time.Now()
		p.mu.Unlock()
		p.logger.Info("mdns: advertising", "names", announcements, "ip", ip)
	}
}

// startReader answers queries on socket until the socket closes.
func (p *Publisher) startReader(socket Socket) {
	go func() {
		for {
			msg, from, err := socket.Read()
			if err != nil {
				return // socket closed
			}
			if err := p.handleQuery(socket, msg, from); err != nil {
				p.logger.Warn("mdns: failed to answer query", "error", err)
			}
		}
	}()
}

// handleQuery answers A questions for the advertised names; everything else
// is ignored (the publisher is a host announcer, not a general mDNS server).
func (p *Publisher) handleQuery(socket Socket, msg *dns.Msg, from net.Addr) error {
	p.mu.Lock()
	ip := p.currentIP
	unicast := hasUnicastFlag(msg)
	var names []string
	seen := map[string]struct{}{}
	for _, q := range msg.Question {
		if q.Qtype != dns.TypeA {
			continue
		}
		name := strings.TrimSuffix(q.Name, ".")
		if _, ok := p.live[name]; !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	p.mu.Unlock()

	if len(names) == 0 {
		return nil
	}
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return nil
	}
	records := make([]dns.RR, 0, len(names))
	for _, n := range names {
		records = append(records, aRecord(n, ipAddr))
	}
	resp := answerMsg(msg, records)
	if unicast {
		return socket.Unicast(from, resp)
	}
	return socket.Multicast(resp)
}

// refreshIfDue re-announces all live records once they are a full TTL old,
// keeping resolver caches fresh (RFC 6762 §10.1).
func (p *Publisher) refreshIfDue() {
	p.mu.Lock()
	socket := p.socket
	ip := p.currentIP
	var names []string
	if len(p.live) > 0 && time.Since(p.lastAnnounce) >= time.Duration(recordTTL)*time.Second {
		names = p.liveNamesLocked()
		p.lastAnnounce = time.Now()
	}
	p.mu.Unlock()
	if socket == nil || len(names) == 0 {
		return
	}
	ipAddr := net.ParseIP(ip)
	records := make([]dns.RR, 0, len(names))
	for _, n := range names {
		records = append(records, aRecord(n, ipAddr))
	}
	if err := socket.Multicast(recordsMsg(records, false)); err != nil {
		p.logger.Warn("mdns: re-announce failed", "error", err)
	}
}

// announce sends the A records for names twice, burstGap apart (RFC 6762
// §8.3.1 initial announcement sequence).
func (p *Publisher) announce(socket Socket, ip string, names []string) {
	ipAddr := net.ParseIP(ip)
	records := make([]dns.RR, 0, len(names))
	for _, n := range names {
		records = append(records, aRecord(n, ipAddr))
	}
	msg := recordsMsg(records, false)
	if err := socket.Multicast(msg); err != nil {
		p.logger.Warn("mdns: announce failed", "error", err)
		return
	}
	time.Sleep(burstGap)
	if err := socket.Multicast(msg); err != nil {
		p.logger.Warn("mdns: announce retransmit failed", "error", err)
	}
}

// sendGoodbyes withdraws names with TTL-0 records (RFC 6762 §10.1) so
// resolvers drop them immediately.
func (p *Publisher) sendGoodbyes(socket Socket, ip string, names []string) {
	if socket == nil || len(names) == 0 {
		return
	}
	ipAddr := net.ParseIP(ip)
	if ipAddr == nil {
		return
	}
	records := make([]dns.RR, 0, len(names))
	for _, n := range names {
		records = append(records, aRecord(n, ipAddr))
	}
	if err := socket.Multicast(recordsMsg(records, true)); err != nil {
		p.logger.Warn("mdns: goodbye failed", "error", err)
	}
}

// Close sends goodbyes for every advertised name and closes the socket.
// Idempotent; safe to call after Run has returned.
func (p *Publisher) Close() {
	p.mu.Lock()
	socket := p.socket
	ip := p.currentIP
	var goodbyes []string
	if socket != nil {
		goodbyes = p.liveNamesLocked()
	}
	p.socket, p.currentIP, p.live = nil, "", map[string]struct{}{}
	p.mu.Unlock()

	if socket != nil {
		p.sendGoodbyes(socket, ip, goodbyes)
		_ = socket.Close()
	}
}

// liveNamesLocked returns the advertised names; caller holds p.mu.
func (p *Publisher) liveNamesLocked() []string {
	names := make([]string, 0, len(p.live))
	for n := range p.live {
		names = append(names, n)
	}
	return names
}
