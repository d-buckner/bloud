// SPDX-License-Identifier: AGPL-3.0-only

package jellyfinmcp

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

	// initializedNotification is the second half of the handshake. A server
	// that has answered `initialize` but not been told the client is ready
	// refuses every other method with "invalid during session initialization",
	// so a probe that skips it can never get as far as a tool.
	initializedNotification = `{"jsonrpc":"2.0","method":"notifications/initialized"}`

	// sessionHeader carries the id `initialize` hands back, and every later
	// request in that session must present it. It is a header rather than part
	// of the envelope, which is why the probe needs header capture at all.
	sessionHeader = "Mcp-Session-Id"

	// toolCallRequest is the probe that actually reaches the media server: one
	// real tool call, `jellyfin_system_info` with `action: info`, which is a
	// read of /System/Info through the wrapper's own client.
	//
	// This is the probe the handshake cannot be. The wrapper answers /health and
	// completes the MCP handshake whether or not its Jellyfin credential works:
	// it reads the unauthenticated /System/Info/Public at startup and logs
	// "Connected to Jellyfin" either way. Measured with a deliberately wrong
	// API key, the container came up healthy, logged the same connection line,
	// and every tool call failed. The break this app exists to fix was exactly a
	// credential the provider refused, so the readiness gate has to be a call
	// that can only succeed by going through it.
	toolCallRequest = `{"jsonrpc":"2.0","id":2,"method":"tools/call",` +
		`"params":{"name":"jellyfin_system_info","arguments":{"action":"info"}}}`
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
func (a *mcpAPI) waitServing(ctx context.Context, bearer string) error {
	_, err := a.openSession(ctx, bearer)
	return err
}

// openSession completes the MCP handshake and returns the session id the
// server issued, so a follow-up call can be made inside the same session.
//
// The bearer is the app's own MCP credential, and presenting it is part of
// what the probe proves. This server's authentication is configured, not
// compiled in, so a probe without the bearer would report a healthy node even
// if the listener had come up with no verification at all. An authorized
// handshake is the only evidence that the credential Bloud published is the
// credential this listener accepts.
//
// The budget is short on purpose. The port binds as soon as the process is
// up, and the container health check already covers that window, so by the
// time PostStart runs the endpoint is answering. What is left to wait for is
// a process that is up but not yet serving.
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
	if session == "" {
		// A server that answers the handshake without naming a session is one
		// this probe cannot drive any further, and saying so beats a follow-up
		// failure that names the wrong thing.
		return "", fmt.Errorf("waiting for the %s server: it completed the MCP handshake but issued no %s, so no tool call can be made", appName, sessionHeader)
	}

	// The handshake is not finished until the server is told the client is
	// ready. A notification has no id and no response body, so its status is
	// the whole answer.
	note := a.cl.POST(mcpEndpoint).
		Body([]byte(initializedNotification), "application/json").
		Header("Accept", acceptStreams).
		Header(sessionHeader, session)
	if bearer != "" {
		note = note.Header("Authorization", "Bearer "+bearer)
	}
	if err := note.Exec(ctx); err != nil {
		return "", fmt.Errorf("completing the %s MCP session: %w", appName, err)
	}
	return session, nil
}

// waitMediaServerReached opens a session and calls one real tool through it,
// polling until the tool answers with content. This is the functional gate:
// it is the only check in this app that can fail for a reason on the Jellyfin
// side, which is the side that broke.
func (a *mcpAPI) waitMediaServerReached(ctx context.Context, bearer string) error {
	session, err := a.openSession(ctx, bearer)
	if err != nil {
		return err
	}
	return a.cl.POST(mcpEndpoint).
		Body([]byte(toolCallRequest), "application/json").
		Header("Accept", acceptStreams).
		Header(sessionHeader, session).
		Header("Authorization", "Bearer "+bearer).
		Wait(toolCallReachedMediaServer).Within(30 * time.Second).Do(ctx)
}

// toolCallReachedMediaServer reports whether a `tools/call` response is a tool
// that actually ran and came back with something.
//
// `isError` is the part that matters. A tool whose provider call failed is not
// a transport failure: the wrapper answers 200 with a well-formed JSON-RPC
// result and puts the provider's refusal inside it (`isError: true`, "Jellyfin
// API error: API error 401 ..."). Reading only the status, or only the absence
// of a JSON-RPC `error`, promotes a node whose every tool call is a refusal.
func toolCallReachedMediaServer(status int, body []byte) bool {
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
	// And it has to have brought something back. A result that is neither an
	// error nor an answer is not a tool that reached its provider.
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		return false
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		return false
	}
	text, _ := first["text"].(string)
	return text != ""
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
