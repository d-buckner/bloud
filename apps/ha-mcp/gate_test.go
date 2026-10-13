// SPDX-License-Identifier: AGPL-3.0-only

package hamcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// TestRenderNginxConfigIsDefaultDeny pins the three properties the gate is for:
// the bearer comparison, no route to the server on any other path, and the
// internal path never being the published one.
func TestRenderNginxConfigIsDefaultDeny(t *testing.T) {
	conf, err := renderNginxConfig("bearer-value", "/mcp-internal")
	require.NoError(t, err)

	require.Contains(t, conf, `if ($http_authorization != "Bearer bearer-value") { return 401; }`)
	require.Contains(t, conf, "location = /mcp {")
	require.Contains(t, conf, "location / { return 404; }")
	require.Contains(t, conf, "rewrite ^ /mcp-internal break;")
	require.NotContains(t, conf, "location = /mcp-internal")
	// The placeholder the container's entrypoint substitutes at start.
	require.Contains(t, conf, "resolver @RESOLVER@ valid=10s ipv6=off;")
	// A streamable-HTTP response is a held-open stream: buffering it would hold
	// the response until the upstream finished, which is never.
	require.Contains(t, conf, "proxy_buffering off;")
}

// TestRenderNginxConfigLockedHasNoRoute: before the credentials exist the gate
// must refuse rather than proxy. Rendering the comparison with an empty bearer
// would match nothing and serve everything, which is the one configuration worse
// than refusing.
func TestRenderNginxConfigLockedHasNoRoute(t *testing.T) {
	for _, tc := range [][2]string{{"", "/mcp-x"}, {"bearer", ""}, {"", ""}} {
		conf, err := renderNginxConfig(tc[0], tc[1])
		require.NoError(t, err)
		require.NotContains(t, conf, "proxy_pass")
		require.Contains(t, conf, "location = /mcp { return 401; }")
	}
}

// TestRenderNginxConfigRefusesAnUnsafeLiteral: both values are generated as
// base64url, so anything else means the config would mean something other than
// what the value says.
func TestRenderNginxConfigRefusesAnUnsafeLiteral(t *testing.T) {
	_, err := renderNginxConfig(`bearer"; drop`, "/mcp-x")
	require.Error(t, err)

	_, err = renderNginxConfig("bearer", "/mcp x")
	require.Error(t, err)
}

// TestGatePreStartWritesAndSettles covers the gate node's own pass: the config
// lands, and a second pass asks for no recreate.
func TestGatePreStartWritesAndSettles(t *testing.T) {
	secrets := newFakeSecrets()
	c := NewGateConfigurator(0, configurator.Deps{Secrets: secrets})
	state := boundState(t.TempDir())

	first, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded)

	conf, err := os.ReadFile(filepath.Join(state.DataPath, configDir, nginxFileName))
	require.NoError(t, err)
	require.Contains(t, string(conf), "Bearer "+secrets.values[httpTokenKey])

	second, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.False(t, second.RestartNeeded)
}

// TestGatePostStartRequiresTheHandshake runs the probe against a stub that only
// answers a successful MCP `initialize`, and only to the right bearer. A probe
// that did not present the credential would report a healthy node that every
// consumer is about to be refused by.
func TestGatePostStartRequiresTheHandshake(t *testing.T) {
	bearer := "gate-bearer"
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Method+" "+r.URL.Path)
		if r.URL.Path != mcpPublicPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+bearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"serverInfo\":{\"name\":\"ha-mcp\",\"version\":\"8.6.0\"}}}\n\n"))
	}))
	t.Cleanup(srv.Close)

	secrets := newFakeSecrets()
	secrets.values[httpTokenKey] = bearer
	c := NewGateConfigurator(0, configurator.Deps{Secrets: secrets})
	c.api = newAPI(configurator.ClientFactory{}, func() string { return srv.URL })

	require.NoError(t, c.PostStart(context.Background(), nil))
	require.Contains(t, got, "POST "+mcpPublicPath)
}

// TestGatePostStartFailsOnAGateThatCannotReachTheServer: 502 is nginx saying the
// upstream is gone. Promoting the node on that answer would leave a harness
// pointed at an endpoint that answers nothing.
func TestGatePostStartFailsOnAGateThatCannotReachTheServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	c := NewGateConfigurator(0, configurator.Deps{})
	c.api = newAPI(configurator.ClientFactory{}, func() string { return srv.URL })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.Error(t, c.PostStart(ctx, nil))
}

// TestMCPHandshakeOK covers the envelope reading: SSE framing, bare JSON, a
// JSON-RPC error, and a result with no server identity.
func TestMCPHandshakeOK(t *testing.T) {
	good := `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"ha-mcp"}}}`
	sse := "event: message\ndata: " + good + "\n\n"
	require.True(t, mcpHandshakeOK(http.StatusOK, []byte(good)))
	require.True(t, mcpHandshakeOK(http.StatusOK, []byte(sse)))
	require.False(t, mcpHandshakeOK(http.StatusUnauthorized, []byte(good)))
	require.False(t, mcpHandshakeOK(http.StatusBadGateway, []byte("502 Bad Gateway")))
	require.False(t, mcpHandshakeOK(http.StatusOK, []byte(`{"error":{"code":-32600,"message":"nope"}}`)))
	require.False(t, mcpHandshakeOK(http.StatusOK, []byte(`{"result":{}}`)))
	require.False(t, mcpHandshakeOK(http.StatusOK, []byte("")))
}

// TestNginxSafeLiteral pins the charset the renderer accepts: URL-safe base64,
// with the leading slash allowed because the endpoint path carries one.
func TestNginxSafeLiteral(t *testing.T) {
	require.True(t, nginxSafeLiteral("abcDEF_012-345"))
	require.True(t, nginxSafeLiteral("/mcp-abcDEF_012"))
	for _, bad := range []string{"", "with space", `quote"`, "back\\slash", "semi;", "new\nline", "dollar$"} {
		require.Falsef(t, nginxSafeLiteral(bad), "%q must not be accepted", bad)
	}
}

// TestHomeAssistantBindingHelpers: an installed provider with an empty password
// is "not ready", and must not be read as an empty credential.
func TestHomeAssistantBindingHelpers(t *testing.T) {
	url, ok := homeAssistantURL(boundState(t.TempDir()))
	require.True(t, ok)
	require.Equal(t, "http://apps-homeassistant:8123", url)

	token, ok := homeAssistantToken(boundState(t.TempDir()))
	require.True(t, ok)
	require.Equal(t, "ha-long-lived-token", token)

	notReady := &configurator.AppState{Integrations: configurator.Integrations{
		AppAPIs: []configurator.AppAPIBinding{{ProviderRef: configurator.ProviderRef{App: "homeassistant", Installed: true}}},
	}}
	_, ok = homeAssistantToken(notReady)
	require.False(t, ok, "an empty published secret is not a credential")

	_, ok = homeAssistantURL(nil)
	require.False(t, ok)
}

// TestSafeSingleQuoted keeps the env renderer's rule explicit.
func TestSafeSingleQuoted(t *testing.T) {
	require.True(t, safeSingleQuoted("http://x:8123"))
	require.False(t, safeSingleQuoted("a'b"))
	require.False(t, safeSingleQuoted("a\nb"))
	require.False(t, safeSingleQuoted("a\rb"))
}
