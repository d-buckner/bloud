// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

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
	// mcpEndpoint is the streamable-HTTP path the server exposes. Probing it
	// rather than its own health path is the point: this is the same endpoint a
	// harness calls, so an answer here is the only evidence that the server
	// can actually serve MCP and not merely answer a liveness ping.
	mcpEndpoint = "/mcp"

	// acceptStreams is what an MCP streamable-HTTP client must offer. A server
	// that gets anything narrower will not answer, so the probe has to ask the
	// way a real client does.
	acceptStreams = "application/json, text/event-stream"

	// initializeRequest is the MCP handshake. It carries no state-changing
	// payload: it asks the server who it is, which is exactly the fact a
	// readiness probe should require.
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
		`"clientInfo":{"name":"bloud-readiness-probe","version":"1"}}}`
)

// mcpAPI probes the bridged MCP server over the endpoint its consumers use.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the server's own port.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the MCP endpoint until the server completes the handshake
// with its own identity, or the budget runs out.
//
// The bearer is the app's own MCP credential, and presenting it is part of
// what the probe proves. This server's authentication is configured, not
// compiled in, so a probe without the bearer would report a healthy node even
// if the listener had come up with AUTH_TYPE=none and no verification at all.
// An authorized handshake is the only evidence that the credential Bloud
// published is the credential this listener accepts.
//
// The budget is short on purpose. The port binds as soon as the process is
// up, and the container health check already covers that window, so by the
// time PostStart runs the endpoint is answering. What is left to wait for is
// a process that is up but not yet serving.
func (a *mcpAPI) waitServing(ctx context.Context, bearer string) error {
	call := a.cl.POST(mcpEndpoint).
		Body([]byte(initializeRequest), "application/json").
		Header("Accept", acceptStreams)
	if bearer != "" {
		call = call.Header("Authorization", "Bearer "+bearer)
	}
	return call.Wait(mcpHandshakeOK).Within(30 * time.Second).Do(ctx)
}

// mcpHandshakeOK reports whether the response is a successful MCP
// `initialize` result naming a server.
//
// A status code cannot answer this. A server that refuses the bearer answers
// 401 and a server that accepts it answers 200, but a gateway in front of a
// dead child can answer 200 with the failure inside the JSON-RPC envelope, so
// only reading the envelope separates "serving MCP" from "up, refusing".
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
// is the answer; the lines before it are keepalives and progress
// notifications.
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
