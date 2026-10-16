// SPDX-License-Identifier: AGPL-3.0-only

package davmcp

import (
	"net/http"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/pkg/bindings"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
	"codeberg.org/d-buckner/bloud/pkg/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func caldavBinding() bindings.CalDAVBinding {
	return bindings.CalDAVBinding{
		ProviderRef: bindings.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Path: "/",
	}
}

func credential() bindings.AppAPIBinding {
	return bindings.AppAPIBinding{
		ProviderRef: bindings.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Username: "caldav-service",
		Password: "secret-password",
	}
}

// --- env file ---

func TestRenderEnvFileCarriesTheResolvedBindings(t *testing.T) {
	content := renderEnvFile("http://apps-radicale:5232/", true, credential(), true, "bearer-abc")

	assert.Contains(t, content, "CALDAV_SERVER_URL='http://apps-radicale:5232/'")
	assert.Contains(t, content, "CALDAV_USERNAME='caldav-service'")
	assert.Contains(t, content, "CALDAV_PASSWORD='secret-password'")
	assert.Contains(t, content, "BEARER_TOKEN='bearer-abc'")
}

// TestRenderEnvFileOmitsWhatHasNotResolved pins the difference between "absent"
// and "empty". Writing CALDAV_PASSWORD=” would tell the server it was
// configured with a blank credential; omitting the key leaves it unset, which
// the server reads as not configured. Only the second one is true, and only the
// second one lets a later pass restart the container once the binding lands.
func TestRenderEnvFileOmitsWhatHasNotResolved(t *testing.T) {
	content := renderEnvFile("http://apps-radicale:5232/", true, bindings.AppAPIBinding{}, false, "")

	assert.Contains(t, content, "CALDAV_SERVER_URL='http://apps-radicale:5232/'")
	assert.NotContains(t, content, "CALDAV_USERNAME")
	assert.NotContains(t, content, "CALDAV_PASSWORD")
	assert.NotContains(t, content, "BEARER_TOKEN")
	assert.NotContains(t, content, "=''")
}

// TestRenderEnvFileDoesNotEchoARejectedValue: the guard fires on a value that
// cannot be quoted safely. The comment it writes must name the key and never the
// value, because one of these keys is a password and this file sits on disk.
func TestRenderEnvFileDoesNotEchoARejectedValue(t *testing.T) {
	bad := credential()
	bad.Password = "has'quote"
	content := renderEnvFile("http://x/", true, bad, true, "ok-token")

	assert.Contains(t, content, "# CALDAV_PASSWORD omitted")
	assert.NotContains(t, content, "has'quote")
	// The rest of the file still renders; one bad value does not take it down.
	assert.Contains(t, content, "CALDAV_USERNAME='caldav-service'")
	assert.Contains(t, content, "BEARER_TOKEN='ok-token'")
}

// TestRenderEnvFileIsByteStable is what makes the steady-state resync a read-only
// diff: managedfile.Write compares bytes to decide whether to report a change, so
// a renderer that reordered or re-touched anything would drive a container
// recreate on every pass.
func TestRenderEnvFileIsByteStable(t *testing.T) {
	first := renderEnvFile("http://apps-radicale:5232/", true, credential(), true, "bearer-abc")
	second := renderEnvFile("http://apps-radicale:5232/", true, credential(), true, "bearer-abc")
	assert.Equal(t, first, second)
}

// TestEnvFileKeysAreTheOnesUpstreamReads guards the one thing this integration
// cannot type-check: the string names dav-mcp looks for in process.env. A typo
// here is a container that starts and serves nothing.
func TestEnvFileKeysAreTheOnesUpstreamReads(t *testing.T) {
	assert.Equal(t, "CALDAV_SERVER_URL", envServerURLKey)
	assert.Equal(t, "CALDAV_USERNAME", envUsernameKey)
	assert.Equal(t, "CALDAV_PASSWORD", envPasswordKey)
	assert.Equal(t, "BEARER_TOKEN", envBearerKey)
	assert.Equal(t, "env", envFileName)
	assert.Equal(t, "config", configDir)
}

func TestRenderEnvFileEveryLineIsAnAssignment(t *testing.T) {
	content := renderEnvFile("http://apps-radicale:5232/", true, credential(), true, "bearer-abc")
	for i, line := range strings.Split(strings.TrimSpace(content), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		assert.Contains(t, line, "=", "line %d is not a KEY=value assignment: %q", i+1, line)
	}
}

// --- readiness probe ---

// Captured from a live dav-mcp 4.1.2 container, verbatim, so these assertions
// are about the wire and not about a fixture invented to match the code.
const (
	// liveHandshake is what a serving dav-mcp answers to `initialize`.
	liveHandshake = ": keepalive\n\nevent: message\n" +
		"data: {\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{}}," +
		"\"serverInfo\":{\"name\":\"dav-mcp\",\"version\":\"4.1.2\"}},\"jsonrpc\":\"2.0\",\"id\":1}\n\n"

	// liveRefusedBearer is what it answers when BEARER_TOKEN is unset: HTTP
	// 500 with the misconfiguration in the envelope.
	liveRefusedBearer = `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,` +
		`"message":"Server misconfiguration: BEARER_TOKEN not set"}}`

	// liveKeepaliveOnly is the preamble with no answer behind it yet.
	liveKeepaliveOnly = ": keepalive\n\n"
)

func TestMCPHandshakeOK(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"serving server", http.StatusOK, liveHandshake, true},
		{"bare JSON result", http.StatusOK,
			`{"result":{"serverInfo":{"name":"dav-mcp"}},"jsonrpc":"2.0","id":1}`, true},
		{"refused bearer is not serving", http.StatusInternalServerError, liveRefusedBearer, false},
		{"keepalive only is not serving", http.StatusOK, liveKeepaliveOnly, false},
		{"result without serverInfo", http.StatusOK, `{"result":{},"jsonrpc":"2.0","id":1}`, false},
		{"empty serverInfo name", http.StatusOK,
			`{"result":{"serverInfo":{"name":""}},"jsonrpc":"2.0","id":1}`, false},
		{"empty body", http.StatusOK, "", false},
		{"not found", http.StatusNotFound, liveHandshake, false},
		{"bad gateway", http.StatusBadGateway, liveHandshake, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mcp.HandshakeOK(tc.status, []byte(tc.body)))
		})
	}
}

// TestMCPHandshakeOKRejectsTheLivenessPayload: /health answers a plain status
// document. Reading that as a handshake would make the probe circular, since the
// whole reason for the handshake is that the liveness path says nothing about
// whether MCP works.
func TestMCPHandshakeOKRejectsTheLivenessPayload(t *testing.T) {
	healthBody := `{"status":"healthy","server":"dav-mcp","version":"4.1.2","transport":"http-stateless"}`
	assert.False(t, mcp.HandshakeOK(http.StatusOK, []byte(healthBody)))
	assert.False(t, mcp.HandshakeOK(http.StatusOK, []byte("ok")))
}

func TestJSONRPCMessageReadsBothFramings(t *testing.T) {
	sse, ok := mcp.JSONRPCMessage([]byte("event: message\ndata: {\"a\":1}\n"))
	require.True(t, ok)
	assert.EqualValues(t, 1, sse["a"])

	bare, ok := mcp.JSONRPCMessage([]byte(`{"b":2}`))
	require.True(t, ok)
	assert.EqualValues(t, 2, bare["b"])

	_, ok = mcp.JSONRPCMessage([]byte(": keepalive\n\nevent: message\n"))
	assert.False(t, ok, "a keepalive carries no envelope")

	_, ok = mcp.JSONRPCMessage([]byte("not json at all"))
	assert.False(t, ok)
}

func TestJSONRPCMessageTakesTheLastDataLine(t *testing.T) {
	body := "data: {\"seq\":1}\ndata: {\"seq\":2}\n"
	msg, ok := mcp.JSONRPCMessage([]byte(body))
	require.True(t, ok)
	assert.EqualValues(t, 2, msg["seq"], "the answer is the last frame, the earlier ones are preamble")
}

// --- bindings ---

func TestCaldavBaseURLConsidersOnlyUsableProviders(t *testing.T) {
	baseURL, ok := caldavBaseURL(&configurator.AppState{
		Integrations: bindings.Integrations{
			CalDAVServers: []bindings.CalDAVBinding{caldavBinding()},
		},
	})
	require.True(t, ok)
	assert.Equal(t, "http://apps-radicale:5232/", baseURL)

	notInstalled := caldavBinding()
	notInstalled.Installed = false
	_, ok = caldavBaseURL(&configurator.AppState{
		Integrations: bindings.Integrations{CalDAVServers: []bindings.CalDAVBinding{notInstalled}},
	})
	assert.False(t, ok, "an uninstalled provider has no address to dial")
}

func TestCaldavCredentialRequiresTheWholeCredential(t *testing.T) {
	cred, ok := caldavCredential(&configurator.AppState{
		Integrations: bindings.Integrations{AppAPIs: []bindings.AppAPIBinding{credential()}},
	})
	require.True(t, ok)
	assert.Equal(t, "caldav-service", cred.Username)
	assert.Equal(t, "secret-password", cred.Password.String())

	noPassword := credential()
	noPassword.Password = ""
	_, ok = caldavCredential(&configurator.AppState{
		Integrations: bindings.Integrations{AppAPIs: []bindings.AppAPIBinding{noPassword}},
	})
	assert.False(t, ok, "an empty password is the provider's 'not published yet' state")
}

func TestMCPHandshakeOKRejectsUnauthorized(t *testing.T) {
	assert.False(t, mcp.HandshakeOK(http.StatusUnauthorized, []byte(liveHandshake)))
}
