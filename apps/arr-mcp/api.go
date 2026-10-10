// SPDX-License-Identifier: AGPL-3.0-only

package arrmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// mcpEndpoint is the streamable-HTTP path the server exposes. Probing it
	// rather than the image's own /healthz is the point: /healthz answers 200
	// even when the config failed validation, while /mcp answers 503 in that
	// state and 401 without the right bearer, so a successful handshake is the
	// evidence that the config parsed and the credential Bloud published is the
	// credential this listener accepts.
	mcpEndpoint = "/mcp"

	// acceptStreams is what an MCP streamable-HTTP client must offer.
	acceptStreams = "application/json, text/event-stream"

	// initializeRequest is the MCP handshake.
	initializeRequest = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
		`"params":{"protocolVersion":"2024-11-05","capabilities":{},` +
		`"clientInfo":{"name":"bloud-readiness-probe","version":"1"}}}`

	// initializedNotification is the second half of the handshake.
	initializedNotification = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

	// sessionHeader carries the id `initialize` hands back.
	sessionHeader = "Mcp-Session-Id"

	// stackHealthRequest is the functional gate: one read-only tool call that
	// reports every configured service's health. A config that parses but holds
	// a credential the service refuses only shows up here, as a degraded entry.
	stackHealthRequest = `{"jsonrpc":"2.0","id":2,"method":"tools/call",` +
		`"params":{"name":"stack_health","arguments":{"detail":"minimal"}}}`
)

// mcpAPI probes the MCP server over the endpoint its consumers use.
type mcpAPI struct {
	cl *appclient.Client
}

// newAPI builds the probe client against the server's own port.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *mcpAPI {
	return &mcpAPI{cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn})}
}

// waitServing polls the MCP endpoint until the server completes the handshake
// with its own identity, or the budget runs out.
func (a *mcpAPI) waitServing(ctx context.Context, bearer string) error {
	_, err := a.openSession(ctx, bearer)
	return err
}

// openSession completes the MCP handshake and returns the session id the
// server issued, so a follow-up call can be made inside the same session.
//
// The bearer is the app's own MCP credential, and presenting it is part of
// what the probe proves: the listener's authentication is configured, not
// compiled in, so a probe without the bearer would report a healthy node even
// if the listener had come up with no verification at all.
func (a *mcpAPI) openSession(ctx context.Context, bearer string) (string, error) {
	var session string
	call := a.cl.POST(mcpEndpoint).
		Body([]byte(initializeRequest), "application/json").
		Header("Accept", acceptStreams).
		CaptureHeader(sessionHeader, &session)
	if bearer != "" {
		call = call.Header("Authorization", "Bearer "+bearer)
	}
	if err := call.Wait(mcpHandshakeOK).Within(30 * time.Second).Do(ctx); err != nil {
		return "", err
	}

	// A server may run stateless: arr-mcp answers `initialize` without a
	// Mcp-Session-Id header, while jellyfin-mcp issues one. Absence is a valid
	// mode, not a fault, so the notification and any follow-up call attach the
	// header only when one was issued.
	note := a.cl.POST(mcpEndpoint).
		Body([]byte(initializedNotification), "application/json").
		Header("Accept", acceptStreams)
	if session != "" {
		note = note.Header(sessionHeader, session)
	}
	if bearer != "" {
		note = note.Header("Authorization", "Bearer "+bearer)
	}
	if err := note.Exec(ctx); err != nil {
		return "", fmt.Errorf("completing the %s MCP session: %w", appName, err)
	}
	return session, nil
}

// waitStackHealthy opens a session and calls stack_health through it, polling
// until the tool answers with at least one healthy service and no degraded
// entries. This is the gate that catches a service credential the config
// parsed but the service refuses.
func (a *mcpAPI) waitStackHealthy(ctx context.Context, bearer string) error {
	session, err := a.openSession(ctx, bearer)
	if err != nil {
		return err
	}
	call := a.cl.POST(mcpEndpoint).
		Body([]byte(stackHealthRequest), "application/json").
		Header("Accept", acceptStreams)
	if session != "" {
		call = call.Header(sessionHeader, session)
	}
	if bearer != "" {
		call = call.Header("Authorization", "Bearer "+bearer)
	}
	return call.Wait(stackHealthOK).Within(30 * time.Second).Do(ctx)
}

// stackHealthOK reports whether a `tools/call` of stack_health came back with
// at least one configured service and nothing in the degraded list.
//
// `isError` is the part that matters most: a tool whose provider call failed
// is answered 200 with a JSON-RPC result carrying `isError: true`, so reading
// only the status promotes a node whose every tool call is a refusal. Beyond
// that, an empty `services` list means nothing was wired (which for a required
// request manager is a real fault), and a non-empty `degraded` list means a
// service is unreachable or its credential was refused.
func stackHealthOK(status int, body []byte) bool {
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
	if failed, _ := result["isError"].(bool); failed {
		return false
	}
	structured, _ := result["structuredContent"].(map[string]any)
	services, _ := structured["services"].([]any)
	if len(services) == 0 {
		return false
	}
	degraded, _ := structured["degraded"].([]any)
	return len(degraded) == 0
}

// mcpHandshakeOK reports whether the response is a successful MCP
// `initialize` result naming a server.
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
// arrive as bare JSON or as a server-sent-events stream.
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
