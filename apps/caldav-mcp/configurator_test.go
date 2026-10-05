// SPDX-License-Identifier: AGPL-3.0-only

package caldavmcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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

// --- run script ---

func TestRenderRunScriptExecutesTheInstalledPackage(t *testing.T) {
	content, err := renderRunScript("http://apps-radicale:5232/", true, credential(), true)
	require.NoError(t, err)

	assert.Contains(t, content, "#!/bin/sh")
	assert.Contains(t, content, "export CALDAV_BASE_URL='http://apps-radicale:5232/'")
	assert.Contains(t, content, "export CALDAV_USERNAME='caldav-service'")
	assert.Contains(t, content, "export CALDAV_PASSWORD='secret-password'")
	assert.Contains(t, content, "exec node "+caldavEntry)
}

// TestRunScriptNeverGoesThroughTheResolver is the regression guard for the
// reported failure: `npx -y` resolves the package against the npm registry on
// every spawn, which put a multi-second network round trip on the first MCP
// `initialize` and exceeded the client's connect timeout. Nothing in either
// generated script may reach for a resolver at spawn time.
func TestRunScriptNeverGoesThroughTheResolver(t *testing.T) {
	run, err := renderRunScript("http://apps-radicale:5232/", true, credential(), true)
	require.NoError(t, err)

	scripts := map[string]string{
		"run.sh":        run,
		"entrypoint.sh": renderEntrypointScript(),
	}
	for name, script := range scripts {
		for _, forbidden := range []string{"npx", "npm exec", "npm start"} {
			assert.NotContains(t, script, forbidden,
				"%s must not resolve the package at spawn time; it runs on the MCP connect path", name)
		}
	}
}

func TestRenderRunScriptOmitsWhatIsNotReady(t *testing.T) {
	content, err := renderRunScript("http://apps-radicale:5232/", true, configurator.AppAPIBinding{}, false)
	require.NoError(t, err)

	assert.Contains(t, content, "export CALDAV_BASE_URL='http://apps-radicale:5232/'")
	assert.NotContains(t, content, "CALDAV_USERNAME")
	assert.NotContains(t, content, "CALDAV_PASSWORD")
	assert.Contains(t, content, "exec node "+caldavEntry)
}

// --- runtime manifest ---

func TestRenderRuntimeManifestPinsTheExactVersion(t *testing.T) {
	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(renderRuntimeManifest()), &manifest),
		"the manifest has to be valid JSON or the entrypoint install cannot read it")

	assert.Equal(t, true, manifest["private"], "the prefix is not a publishable package")
	deps, ok := manifest["dependencies"].(map[string]any)
	require.True(t, ok, "the pinned dependency map is missing")
	assert.Equal(t, caldavVersion, deps[caldavPackage],
		"an exact pin is what makes the installed tree the tree the pin names")
	assert.NotContains(t, caldavVersion, "^")
	assert.NotContains(t, caldavVersion, "~")
}

// --- entrypoint script ---

func TestRenderEntrypointScriptInstallsOnceThenExecsGateway(t *testing.T) {
	script := renderEntrypointScript()

	assert.Contains(t, script, "#!/bin/sh")
	assert.Contains(t, script, "set -eu", "a failed install must stop the container, not start a gateway with nothing bridged")
	assert.Contains(t, script, "PREFIX='"+runtimePrefix+"'",
		"the install targets the persistent prefix the volume mounts, not the image")
	assert.Contains(t, script, "npm install --prefix \"$PREFIX\"",
		"the install writes into the prefix rather than the image's own tree")
	assert.Contains(t, script, "exec supergateway \"$@\"",
		"the gateway gets the flags metadata.yaml passes, so the bridge config stays in one place")
	assert.Contains(t, script, caldavVersion)
}

// TestEntrypointMarkerIsNamedForThePin covers the upgrade path: a marker that
// does not carry the version would let a bumped pin keep running the previous
// tree forever.
func TestEntrypointMarkerIsNamedForThePin(t *testing.T) {
	script := renderEntrypointScript()
	assert.Contains(t, script, ".installed-"+caldavVersion)
	assert.Contains(t, script, "! -f \"$ENTRY\"",
		"a half-populated prefix must reinstall rather than exec a missing entry file")
}

// --- readiness probe ---

// Captured from a live supergateway bridge, verbatim, so these assertions are
// about the wire, not about a fixture invented to match the code.
const (
	// liveHandshake is what a serving bridge answers.
	liveHandshake = "event: message\n" +
		"data: {\"result\":{\"protocolVersion\":\"2024-11-05\",\"capabilities\":{\"tools\":{\"listChanged\":true}}," +
		"\"serverInfo\":{\"name\":\"caldav-mcp\",\"version\":\"0.1.0\"}},\"jsonrpc\":\"2.0\",\"id\":1}\n"

	// liveDeadChild is what the gateway answers when the bridged child cannot
	// start. HTTP 200, so only the envelope tells them apart.
	liveDeadChild = "event: message\n" +
		"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32603,\"message\":\"MCP server process failed\"}}\n"

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
		{"serving bridge", http.StatusOK, liveHandshake, true},
		{"bare JSON result", http.StatusOK,
			`{"result":{"serverInfo":{"name":"caldav-mcp"}},"jsonrpc":"2.0","id":1}`, true},
		{"dead child is not serving", http.StatusOK, liveDeadChild, false},
		{"keepalive only is not serving", http.StatusOK, liveKeepaliveOnly, false},
		{"result without serverInfo", http.StatusOK, `{"result":{},"jsonrpc":"2.0","id":1}`, false},
		{"empty serverInfo name", http.StatusOK,
			`{"result":{"serverInfo":{"name":""}},"jsonrpc":"2.0","id":1}`, false},
		{"empty body", http.StatusOK, "", false},
		{"not found", http.StatusNotFound, liveHandshake, false},
		{"gateway error", http.StatusBadGateway, liveHandshake, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, mcpHandshakeOK(tc.status, []byte(tc.body)))
		})
	}
}

func TestMCPHandshakeOKRejectsTheGatewayLivenessPayload(t *testing.T) {
	// The old probe accepted "ok" from /healthz. It must not read as a
	// handshake, or the fix would be circular.
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

// --- pin consistency ---

func TestPinCoordinatesAgree(t *testing.T) {
	assert.Equal(t, "caldav-mcp@0.10.0", caldavPin)
	assert.True(t, strings.HasPrefix(caldavEntry, runtimePrefix),
		"the entry lives inside the persistent prefix the volume mounts")
	assert.Contains(t, caldavEntry, caldavPackage)
}
