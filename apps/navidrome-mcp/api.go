// SPDX-License-Identifier: AGPL-3.0-only

package navidromemcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

const (
	// mcpPath is the endpoint the app publishes under its `mcp` offer, and the
	// path the image serves the streamable-HTTP transport on.
	mcpPath = "/mcp"

	// acceptStreams is what an MCP streamable-HTTP client must offer. A server
	// that gets anything narrower will not answer, so the probe has to ask the
	// way a real client does.
	acceptStreams = "application/json, text/event-stream"

	// initializeRequest is the MCP handshake. It changes no library state: it
	// asks the server who it is, which is exactly the fact a readiness probe
	// should require.
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
		`"clientInfo":{"name":"bloud-readiness-probe","version":"1"}}}`
)

// mcpAPI probes the app's MCP endpoint the way a consumer calls it.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the address the app publishes.
func newAPI(f configurator.HTTPFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the published MCP path until the server completes the
// handshake with its own identity, or the budget runs out.
//
// The budget is short on purpose: the container has passed its own health check by
// the time this runs, so what is left to wait for is a process that is up but not
// yet serving, not a cold start. The cold start is the health check's job, and it
// carries the generous retries because this image binds its socket only after it
// has signed in to Navidrome and primed its caches.
func (a *mcpAPI) waitServing(ctx context.Context, bearer string) error {
	call := a.cl.POST(mcpPath).
		Body([]byte(initializeRequest), "application/json").
		Header("Accept", acceptStreams)
	if bearer != "" {
		call = call.Header("Authorization", "Bearer "+bearer)
	}
	return call.Wait(mcpHandshakeOK).Within(60 * time.Second).Do(ctx)
}

// mcpHandshakeOK reports whether the response is a successful MCP `initialize`
// result naming a server.
//
// A status code cannot answer this. A server that is up but cannot reach its
// music library still answers the handshake, and one that rejects the bearer
// answers 401; only reading the envelope separates "serving MCP for someone" from
// "something is listening".
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
// `data: {...}`). Under SSE framing the last data line carrying a JSON object is
// the answer; the lines before it are keepalives.
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
