package sharing

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// ProxyTarget describes a remote app that needs a local reverse proxy.
type ProxyTarget struct {
	ID         string // unique slug (e.g. "jellyfin-johan")
	TailnetURL string // full URL to the remote sidecar (e.g. "https://ts-jellyfin.tail1275sa.ts.net")
}

// RemoteProxyManager manages lightweight HTTP reverse proxies for remote apps.
// Each proxy listens on a localhost port and dials through the gateway's SOCKS5
// proxy to reach the remote sidecar's tailnet address.
type RemoteProxyManager struct {
	socksAddr string // SOCKS5 proxy address (e.g. "localhost:1055")
	basePort  int    // starting port for allocation (e.g. 10100)
	mu        sync.Mutex
	proxies   map[string]*runningProxy // remote app ID -> proxy
	logger    *slog.Logger
}

type runningProxy struct {
	port     int
	listener net.Listener
	server   *http.Server
}

// NewRemoteProxyManager creates a RemoteProxyManager.
//   - socksAddr: gateway SOCKS5 proxy address (e.g. "localhost:1055").
//   - basePort: starting port number for proxy allocation.
func NewRemoteProxyManager(socksAddr string, basePort int, logger *slog.Logger) *RemoteProxyManager {
	return &RemoteProxyManager{
		socksAddr: socksAddr,
		basePort:  basePort,
		proxies:   make(map[string]*runningProxy),
		logger:    logger,
	}
}

// Reconcile syncs running proxies with the desired set of targets.
// It starts new proxies for added targets and stops proxies for removed ones.
// Returns a map of target ID -> localhost port for all active proxies.
func (m *RemoteProxyManager) Reconcile(targets []ProxyTarget) map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Deterministic port assignment: sort targets by ID, assign from basePort.
	sorted := make([]ProxyTarget, len(targets))
	copy(sorted, targets)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	desired := make(map[string]ProxyTarget, len(sorted))
	portAssignment := make(map[string]int, len(sorted))
	for i, t := range sorted {
		desired[t.ID] = t
		portAssignment[t.ID] = m.basePort + i
	}

	// Stop proxies that are no longer needed or whose port changed.
	for id, rp := range m.proxies {
		newPort, stillNeeded := portAssignment[id]
		if !stillNeeded || newPort != rp.port {
			m.stopProxyLocked(id)
		}
	}

	// Start proxies for new or changed targets.
	result := make(map[string]int, len(sorted))
	for _, t := range sorted {
		port := portAssignment[t.ID]

		if existing, ok := m.proxies[t.ID]; ok && existing.port == port {
			result[t.ID] = port
			continue
		}

		if err := m.startProxyLocked(t, port); err != nil {
			m.logger.Warn("failed to start reverse proxy", "target", t.ID, "port", port, "error", err)
			continue
		}
		result[t.ID] = port
	}

	return result
}

// StopAll shuts down all running proxies.
func (m *RemoteProxyManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id := range m.proxies {
		m.stopProxyLocked(id)
	}
}

func (m *RemoteProxyManager) startProxyLocked(target ProxyTarget, port int) error {
	targetURL, err := url.Parse(target.TailnetURL)
	if err != nil {
		return fmt.Errorf("parse target URL %q: %w", target.TailnetURL, err)
	}

	dialer, err := proxy.SOCKS5("tcp", m.socksAddr, nil, proxy.Direct)
	if err != nil {
		return fmt.Errorf("create SOCKS5 dialer: %w", err)
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.Dial(network, addr)
		},
		// InsecureSkipVerify is acceptable here: traffic goes through an
		// authenticated Tailscale tunnel, and the remote sidecar serves HTTPS
		// with a Tailscale-issued cert that may not be in the system CA pool.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	rp := httputil.NewSingleHostReverseProxy(targetURL)
	rp.Transport = transport

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen on port %d: %w", port, err)
	}

	server := &http.Server{
		Handler:      rp,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	m.proxies[target.ID] = &runningProxy{
		port:     port,
		listener: listener,
		server:   server,
	}

	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			m.logger.Warn("reverse proxy stopped unexpectedly", "target", target.ID, "port", port, "error", err)
		}
	}()

	m.logger.Info("started reverse proxy", "target", target.ID, "port", port, "tailnetURL", target.TailnetURL)
	return nil
}

func (m *RemoteProxyManager) stopProxyLocked(id string) {
	rp, ok := m.proxies[id]
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rp.server.Shutdown(ctx); err != nil {
		m.logger.Warn("failed to gracefully shut down reverse proxy", "target", id, "error", err)
		rp.listener.Close()
	}

	delete(m.proxies, id)
	m.logger.Info("stopped reverse proxy", "target", id, "port", rp.port)
}
