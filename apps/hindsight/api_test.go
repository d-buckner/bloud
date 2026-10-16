// SPDX-License-Identifier: AGPL-3.0-only

package hindsight

import (
	"testing"

	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/mcp"
)

// fakeHTTP satisfies configurator.HTTPFactory with a real client, so a gate
// built with it has a probe to exercise. The factory exists so a test can tell
// "no probe available" from "probe available"; it is not a mock of the app.
type fakeHTTP struct{}

func (fakeHTTP) New(spec appclient.Spec) *appclient.Client { return appclient.New(spec) }

// initializeResult is the body the real server returns for the handshake the
// probe sends. Captured from ghcr.io/vectorize-io/hindsight:1.0.0 under
// HINDSIGHT_API_LLM_PROVIDER=none, with the tenant extension loaded.
const initializeResult = `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05",` +
	`"capabilities":{"tools":{"listChanged":true}},` +
	`"serverInfo":{"name":"hindsight-mcp-server","version":"1.0.0"}}}`

// TestHandshakeAcceptsTheRealResponse is the positive case against the bytes the
// image actually sends: an SSE-framed JSON-RPC result naming a server.
func TestHandshakeAcceptsTheRealResponse(t *testing.T) {
	sse := "event: message\ndata: " + initializeResult + "\n\n"
	require.True(t, mcp.HandshakeOK(200, []byte(sse)))
	require.True(t, mcp.HandshakeOK(200, []byte(initializeResult)),
		"a server that answers with bare JSON rather than SSE is still serving MCP")
}

// TestHandshakeRefusesEverythingThatIsNotAnAnswer is the reason the probe reads
// the envelope instead of the status code. Each of these is "something is
// listening" and none of them is "this app can serve its tools".
func TestHandshakeRefusesEverythingThatIsNotAnAnswer(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
	}{
		"locked gate":          {status: 401, body: ""},
		"gate up, server gone": {status: 502, body: "Bad Gateway"},
		"empty body":           {status: 200, body: ""},
		"html error page":      {status: 200, body: "<html>working</html>"},
		"json-rpc error": {
			status: 200,
			body:   `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"nope"}}`,
		},
		"result with no server identity": {
			status: 200,
			body:   `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05"}}`,
		},
		"server info with an empty name": {
			status: 200,
			body:   `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":""}}}`,
		},
		"sse keepalive only": {status: 200, body: "event: accepted\ndata: {\"ok\":true}\n"},
	}
	for name, tc := range cases {
		require.Falsef(t, mcp.HandshakeOK(tc.status, []byte(tc.body)), name)
	}
}

// TestJSONRPCMessageTakesTheLastAnswer pins the SSE rule: a stream may carry
// keepalives before the reply, and the last data line carrying a JSON object is
// the one that answers.
func TestJSONRPCMessageTakesTheLastAnswer(t *testing.T) {
	body := "event: message\ndata: {\"keepalive\":true}\n\nevent: message\ndata: " + initializeResult + "\n\n"
	msg, ok := mcp.JSONRPCMessage([]byte(body))
	require.True(t, ok)
	require.Contains(t, msg, "result")

	for _, bad := range []string{"", "   ", "not json", "data: not json"} {
		if _, ok := mcp.JSONRPCMessage([]byte(bad)); ok {
			t.Fatalf("%q is not a JSON-RPC message", bad)
		}
	}
}
