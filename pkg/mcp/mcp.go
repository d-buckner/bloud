// SPDX-License-Identifier: AGPL-3.0-only

// Package mcp holds the shared streamable-HTTP readiness probe. Every MCP
// wrapper in the catalog proves it is serving with the same initialize
// handshake. This package owns that probe so the five copies stay one.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
)

// AcceptStreams is the Accept header an MCP streamable-HTTP client must offer.
// A server that gets anything narrower will not answer, so the probe asks the
// way a real client does.
const AcceptStreams = "application/json, text/event-stream"

// InitializeRequest is the MCP initialize handshake. It carries no credential
// and changes no state: it asks the server who it is, which is the fact a
// readiness probe needs.
const InitializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
	`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
	`"clientInfo":{"name":"bloud-readiness-probe","version":"1"}}}`

// Probe polls path on cl until the server completes the initialize handshake,
// or within elapses. bearer is the app's own MCP credential and is optional.
// An empty bearer sends no Authorization header, so a server that enforces one
// is not promoted as ready.
func Probe(ctx context.Context, cl *appclient.Client, path, bearer string, within time.Duration) error {
	call := cl.POST(path).
		Body([]byte(InitializeRequest), "application/json").
		Header("Accept", AcceptStreams)
	if bearer != "" {
		call = call.Header("Authorization", "Bearer "+bearer)
	}
	return call.Wait(HandshakeOK).Within(within).Do(ctx)
}

// HandshakeOK reports whether a response is a successful MCP initialize result
// that names a server. A status code cannot answer this: a gate or bridge that
// is up but cannot reach its target still answers 200 and puts the failure in
// the JSON-RPC envelope. Reading the envelope separates "serving MCP" from
// "something is listening".
func HandshakeOK(status int, body []byte) bool {
	if status != http.StatusOK {
		return false
	}
	msg, ok := JSONRPCMessage(body)
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

// JSONRPCMessage pulls the JSON-RPC envelope out of a body that may arrive as
// bare JSON or as a server-sent-events stream (`event: message` /
// `data: {...}`). Under SSE framing the last data line carrying a JSON object
// is the answer.
func JSONRPCMessage(body []byte) (map[string]any, bool) {
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
