// SPDX-License-Identifier: AGPL-3.0-only

package davmcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func caldavBinding() configurator.CalDAVBinding {
	return configurator.CalDAVBinding{
		ProviderRef: configurator.ProviderRef{
			App:       "radicale",
			Installed: true,
			Node:      "apps-radicale",
			Port:      5232,
			BaseURL:   "http://apps-radicale:5232",
		},
		Path: "/",
	}
}

func credential() configurator.AppAPIBinding {
	return configurator.AppAPIBinding{
		ProviderRef: configurator.ProviderRef{
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
	content := renderEnvFile("http://apps-radicale:5232/", true, configurator.AppAPIBinding{}, false, "")

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
			assert.Equal(t, tc.want, mcpHandshakeOK(tc.status, []byte(tc.body)))
		})
	}
}

// TestMCPHandshakeOKRejectsTheLivenessPayload: /health answers a plain status
// document. Reading that as a handshake would make the probe circular, since the
// whole reason for the handshake is that the liveness path says nothing about
// whether MCP works.
func TestMCPHandshakeOKRejectsTheLivenessPayload(t *testing.T) {
	healthBody := `{"status":"healthy","server":"dav-mcp","version":"4.1.2","transport":"http-stateless"}`
	assert.False(t, mcpHandshakeOK(http.StatusOK, []byte(healthBody)))
	assert.False(t, mcpHandshakeOK(http.StatusOK, []byte("ok")))
}

func TestJSONRPCMessageReadsBothFramings(t *testing.T) {
	sse, ok := jsonRPCMessage([]byte("event: message\ndata: {\"a\":1}\n"))
	require.True(t, ok)
	assert.EqualValues(t, 1, sse["a"])

	bare, ok := jsonRPCMessage([]byte(`{"b":2}`))
	require.True(t, ok)
	assert.EqualValues(t, 2, bare["b"])

	_, ok = jsonRPCMessage([]byte(": keepalive\n\nevent: message\n"))
	assert.False(t, ok, "a keepalive carries no envelope")

	_, ok = jsonRPCMessage([]byte("not json at all"))
	assert.False(t, ok)
}

func TestJSONRPCMessageTakesTheLastDataLine(t *testing.T) {
	body := "data: {\"seq\":1}\ndata: {\"seq\":2}\n"
	msg, ok := jsonRPCMessage([]byte(body))
	require.True(t, ok)
	assert.EqualValues(t, 2, msg["seq"], "the answer is the last frame, the earlier ones are preamble")
}

// --- bindings ---

func TestCaldavBaseURLConsidersOnlyUsableProviders(t *testing.T) {
	baseURL, ok := caldavBaseURL(&configurator.AppState{
		Integrations: configurator.Integrations{
			CalDAVServers: []configurator.CalDAVBinding{caldavBinding()},
		},
	})
	require.True(t, ok)
	assert.Equal(t, "http://apps-radicale:5232/", baseURL)

	notInstalled := caldavBinding()
	notInstalled.Installed = false
	_, ok = caldavBaseURL(&configurator.AppState{
		Integrations: configurator.Integrations{CalDAVServers: []configurator.CalDAVBinding{notInstalled}},
	})
	assert.False(t, ok, "an uninstalled provider has no address to dial")
}

func TestCaldavCredentialRequiresTheWholeCredential(t *testing.T) {
	cred, ok := caldavCredential(&configurator.AppState{
		Integrations: configurator.Integrations{AppAPIs: []configurator.AppAPIBinding{credential()}},
	})
	require.True(t, ok)
	assert.Equal(t, "caldav-service", cred.Username)
	assert.Equal(t, "secret-password", cred.Password)

	noPassword := credential()
	noPassword.Password = ""
	_, ok = caldavCredential(&configurator.AppState{
		Integrations: configurator.Integrations{AppAPIs: []configurator.AppAPIBinding{noPassword}},
	})
	assert.False(t, ok, "an empty password is the provider's 'not published yet' state")
}

// --- bearer presentation ---

// storeSecrets is a minimal in-memory AppSecretsProvider.
type storeSecrets struct {
	values map[string]string
	set    []string
}

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (s *storeSecrets) GetAppSecret(_, key string) string {
	return s.values[key]
}
func (s *storeSecrets) SetAppSecret(_, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	s.set = append(s.set, key)
	return nil
}
func (s *storeSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (s *storeSecrets) GetAppContractValue(string, string, string) string        { return "" }

// TestWaitServingPresentsTheBearer is the regression guard for the failure this
// integration actually hit in a live install: the container came up healthy and
// serving 27 tools while every probe was refused with 401, because the probe
// sent no credential and the server enforces one. A green node that every real
// consumer is about to be refused by is worse than a red one.
func TestWaitServingPresentsTheBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"result\":{\"serverInfo\":{\"name\":\"dav-mcp\"," +
			"\"version\":\"4.1.2\"}},\"jsonrpc\":\"2.0\",\"id\":1}\n\n"))
	}))
	defer srv.Close()

	api := newAPI(configurator.ClientFactory{}, func() string { return srv.URL })
	require.NoError(t, api.waitServing(context.Background(), "the-bearer"))
	assert.Equal(t, "Bearer the-bearer", gotAuth)
}

func TestWaitServingSendsNoAuthHeaderWithoutABearer(t *testing.T) {
	var gotAuth string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Unauthorized"}}`))
	}))
	defer srv.Close()

	api := newAPI(configurator.ClientFactory{}, func() string { return srv.URL })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// No bearer means no header, and the wait must fail rather than promote a
	// server that is refusing everything.
	assert.Error(t, api.waitServing(ctx, ""))
	assert.Empty(t, gotAuth)
	assert.Greater(t, calls, 1, "an unauthorized response is not ready, so it retries")
}

func TestMCPHandshakeOKRejectsUnauthorized(t *testing.T) {
	assert.False(t, mcpHandshakeOK(http.StatusUnauthorized, []byte(liveHandshake)))
}

func TestCurrentBearerReadsWithoutGenerating(t *testing.T) {
	secrets := &storeSecrets{values: map[string]string{httpTokenKey: "published-token"}}
	c := NewConfigurator(defaultPort, configurator.Deps{Secrets: secrets})

	assert.Equal(t, "published-token", c.currentBearer())
	assert.Empty(t, secrets.set, "PostStart must not mint a credential, only present the current one")
}

func TestCurrentBearerWithoutSecretsIsEmpty(t *testing.T) {
	c := NewConfigurator(defaultPort, configurator.Deps{})
	assert.Equal(t, "", c.currentBearer())
}
