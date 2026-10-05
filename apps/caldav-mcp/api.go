// SPDX-License-Identifier: AGPL-3.0-only

package caldavmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// mcpEndpoint is the streamable-HTTP path supergateway bridges to the
	// stdio child. Probing it rather than the gateway's own health path is the
	// point: this is the same endpoint a harness calls, so an answer here is
	// the only evidence that the bridged caldav-mcp process actually starts.
	mcpEndpoint = "/mcp"

	// acceptStreams is what an MCP streamable-HTTP client must offer. A server
	// that gets anything narrower will not answer, so the probe has to ask the
	// way a real client does.
	acceptStreams = "application/json, text/event-stream"

	// initializeRequest is the MCP handshake. It carries no credential and
	// changes no state: it asks the server who it is, which is exactly the
	// fact a readiness probe should require.
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
		`"clientInfo":{"name":"bloud-readiness-probe","version":"1"}}}`
)

// mcpAPI probes the bridged MCP server over the endpoint its consumers use.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the gateway's own port.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the MCP endpoint until the bridged server completes the
// handshake with its own identity, or the budget runs out.
//
// The budget covers a cold container start, where the entrypoint installs the
// pinned package before the gateway binds, plus the server's own startup. It is
// not a budget for npm resolution on a session: that happens before the port is
// reachable at all.
func (a *mcpAPI) waitServing(ctx context.Context) error {
	return a.cl.POST(mcpEndpoint).
		Body([]byte(initializeRequest), "application/json").
		Header("Accept", acceptStreams).
		Within(120 * time.Second).
		Ready(mcpHandshakeOK).
		Wait(ctx)
}

// mcpHandshakeOK reports whether the response is a successful MCP `initialize`
// result naming a server.
//
// A status code cannot answer this. When the bridged child fails to start,
// supergateway still answers 200 and puts the failure in the JSON-RPC envelope
// as an `error`, so only reading the envelope separates "serving MCP" from
// "gateway up, server down".
func mcpHandshakeOK(status int, body []byte) bool {
	if status != http.StatusOK {
		return false
	}
	msg, ok := jsonRPCMessage(body)
	if !ok {
		return false
	}
	if _, failed := msg["error"]; failed {
		return false
	}
	result, ok := msg["result"].(map[string]any)
	if !ok {
		return false
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok {
		return false
	}
	name, _ := info["name"].(string)
	return name != ""
}

// jsonRPCMessage pulls the JSON-RPC envelope out of a response body that may
// arrive as bare JSON or as a server-sent-events stream (`event: message` /
// `data: {...}`). Under SSE framing the last data line carrying a JSON object
// is the answer; the lines before it are keepalives.
func jsonRPCMessage(body []byte) (map[string]any, bool) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return nil, false
	}
	if strings.HasPrefix(trimmed, "{") {
		var msg map[string]any
		if err := json.Unmarshal([]byte(trimmed), &msg); err == nil {
			return msg, true
		}
	}
	var last map[string]any
	for _, line := range strings.Split(trimmed, "\n") {
		payload, isData := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !isData {
			continue
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(payload)), &msg); err == nil {
			last = msg
		}
	}
	if last == nil {
		return nil, false
	}
	return last, true
}
