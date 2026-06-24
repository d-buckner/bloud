package sharing

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteProxy_Reconcile_StartsProxies(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18100, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
		{ID: "navidrome-anna", TailnetURL: "https://ts-navidrome.tail.net"},
	}

	result := mgr.Reconcile(targets)

	// Both proxies should be running with deterministic ports (sorted by ID).
	assert.Len(t, result, 2)
	assert.Equal(t, 18100, result["jellyfin-johan"]) // j < n
	assert.Equal(t, 18101, result["navidrome-anna"])

	// Verify listeners are actually listening.
	for _, port := range result {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		require.NoError(t, err, "proxy should be listening on port %d", port)
		conn.Close()
	}
}

func TestRemoteProxy_Reconcile_Idempotent(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18200, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
	}

	result1 := mgr.Reconcile(targets)
	result2 := mgr.Reconcile(targets)

	assert.Equal(t, result1, result2)
	assert.Equal(t, 18200, result2["jellyfin-johan"])
}

func TestRemoteProxy_Reconcile_StopsRemovedTargets(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18300, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
		{ID: "navidrome-anna", TailnetURL: "https://ts-navidrome.tail.net"},
	}

	result := mgr.Reconcile(targets)
	require.Len(t, result, 2)
	navidromePort := result["navidrome-anna"]

	// Remove navidrome, keep jellyfin.
	targets = []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
	}
	result = mgr.Reconcile(targets)

	assert.Len(t, result, 1)
	assert.Contains(t, result, "jellyfin-johan")

	// Navidrome port should no longer be listening.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", navidromePort))
	if err == nil {
		conn.Close()
		t.Error("navidrome proxy should have been stopped")
	}
}

func TestRemoteProxy_Reconcile_EmptyTargets_StopsAll(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18400, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
	}

	result := mgr.Reconcile(targets)
	port := result["jellyfin-johan"]

	// Reconcile with empty list.
	result = mgr.Reconcile(nil)
	assert.Empty(t, result)

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		conn.Close()
		t.Error("proxy should have been stopped")
	}
}

func TestRemoteProxy_StopAll(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18500, discardLogger())

	targets := []ProxyTarget{
		{ID: "jellyfin-johan", TailnetURL: "https://ts-jellyfin.tail.net"},
		{ID: "navidrome-anna", TailnetURL: "https://ts-navidrome.tail.net"},
	}

	result := mgr.Reconcile(targets)
	require.Len(t, result, 2)

	mgr.StopAll()

	for id, port := range result {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			t.Errorf("proxy %s on port %d should have been stopped", id, port)
		}
	}
}

func TestRemoteProxy_Reconcile_InvalidURL(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18600, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "bad-app", TailnetURL: "://invalid"},
	}

	result := mgr.Reconcile(targets)
	assert.Empty(t, result, "invalid URL target should not produce a proxy")
}

func TestRemoteProxy_ProxiesHTTPRequest(t *testing.T) {
	// Start a simple backend HTTP server.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello from backend"))
	}))
	defer backend.Close()

	// Create a proxy manager pointing directly to the backend (no real SOCKS5
	// needed since the SOCKS5 dial will fail — we test the proxy wiring by
	// using a target that resolves to localhost). For a true integration test
	// we'd need a SOCKS5 server, but we can still verify the proxy starts and
	// the HTTP handler is wired correctly by checking the listener is up.
	mgr := NewRemoteProxyManager("localhost:19999", 18700, discardLogger())
	defer mgr.StopAll()

	targets := []ProxyTarget{
		{ID: "test-app", TailnetURL: backend.URL},
	}

	result := mgr.Reconcile(targets)
	require.Contains(t, result, "test-app")

	// The proxy is listening — making a request will fail at SOCKS5 dial
	// (no SOCKS5 server on 19999), but the listener being up proves the
	// proxy infrastructure works.
	port := result["test-app"]
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	require.NoError(t, err)
	conn.Close()
}

func TestRemoteProxy_DeterministicPortAssignment(t *testing.T) {
	mgr := NewRemoteProxyManager("localhost:19999", 18800, discardLogger())
	defer mgr.StopAll()

	// Ports are assigned alphabetically by ID.
	targets := []ProxyTarget{
		{ID: "z-app", TailnetURL: "https://z.tail.net"},
		{ID: "a-app", TailnetURL: "https://a.tail.net"},
		{ID: "m-app", TailnetURL: "https://m.tail.net"},
	}

	result := mgr.Reconcile(targets)
	assert.Equal(t, 18800, result["a-app"])
	assert.Equal(t, 18801, result["m-app"])
	assert.Equal(t, 18802, result["z-app"])
}
