// SPDX-License-Identifier: AGPL-3.0-only

package homeassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// The Home Assistant WebSocket API is the only way to mint a long-lived access
// token (the REST surface has no such endpoint), so this file carries the
// smallest client that can complete one command. It is deliberately narrow:
// connect, authenticate, send commands with sequential ids, read back the
// matching result. Subscriptions, event streams, and fragmentation across many
// frames are not used by any command here and are not implemented.
//
// The protocol constants (auth_required / auth / auth_ok, the id-matched
// result / error envelope) are defined by Home Assistant's
// homeassistant/components/websocket_api and confirmed against the auth
// component's own command handlers; INTEGRATION.md records the source.

const (
	// wsPath is Home Assistant's single WebSocket endpoint.
	wsPath = "/api/websocket"

	// wsHandshakeBudget bounds one command round trip when the caller's
	// context carries no deadline of its own. Home Assistant answers these
	// commands in-process, so anything slower than this is a wedged
	// connection rather than slow work.
	wsHandshakeBudget = 30 * time.Second
)

// wsEnvelope is one WebSocket API message, read loosely: every command
// response carries a subset of these fields, and the fields a given message
// omits stay zero.
type wsEnvelope struct {
	ID      *int            `json:"id"`
	Type    string          `json:"type"`
	Success *bool           `json:"success"`
	Result  json.RawMessage `json:"result"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	Code    string          `json:"code"`
}

// haWS is an authenticated Home Assistant WebSocket connection.
type haWS struct {
	conn *websocket.Conn
	// nextID numbers the commands sent on this connection. Home Assistant
	// echoes the id on the matching response, which is how a reply is matched
	// to the command that caused it.
	nextID int
}

// wsURLFor turns an HTTP base URL into the WebSocket URL of the same instance.
func wsURLFor(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("home assistant base URL %q is unparseable: %w", baseURL, err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("home assistant base URL %q has scheme %q; expected http or https", baseURL, u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + wsPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// haDial opens the WebSocket connection and completes Home Assistant's
// authentication exchange with accessToken, which may be any access token: the
// short-lived one from a login flow or a long-lived one already stored.
func haDial(ctx context.Context, wsURL, accessToken string) (*haWS, error) {
	dialer := websocket.Dialer{HandshakeTimeout: wsHandshakeBudget}
	conn, resp, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("connecting to the Home Assistant WebSocket API at %s: %w (HTTP %d)", wsURL, err, resp.StatusCode)
		}
		return nil, fmt.Errorf("connecting to the Home Assistant WebSocket API at %s: %w", wsURL, err)
	}
	w := &haWS{conn: conn}

	// The server always opens with auth_required. Reading it first is what the
	// protocol requires, and it means a connection that is not Home Assistant
	// (a proxy answering instead) fails here with a clear message instead of
	// timing out on a command nobody answered.
	if err := w.setDeadline(ctx); err != nil {
		_ = w.conn.Close()
		return nil, err
	}
	var first wsEnvelope
	if err := w.conn.ReadJSON(&first); err != nil {
		_ = w.conn.Close()
		return nil, fmt.Errorf("reading the Home Assistant WebSocket hello: %w", err)
	}
	if first.Type != "auth_required" {
		_ = w.conn.Close()
		return nil, fmt.Errorf("unexpected first Home Assistant WebSocket message %q; expected auth_required", first.Type)
	}

	if err := w.write(ctx, map[string]any{"type": "auth", "access_token": accessToken}); err != nil {
		_ = w.conn.Close()
		return nil, err
	}
	if err := w.setDeadline(ctx); err != nil {
		_ = w.conn.Close()
		return nil, err
	}
	var authed wsEnvelope
	if err := w.conn.ReadJSON(&authed); err != nil {
		_ = w.conn.Close()
		return nil, fmt.Errorf("reading the Home Assistant WebSocket auth response: %w", err)
	}
	if authed.Type != "auth_ok" {
		_ = w.conn.Close()
		message := authed.Message
		if message == "" {
			message = authed.Type
		}
		return nil, fmt.Errorf("the access token was refused by Home Assistant over WebSocket: %s", message)
	}
	return w, nil
}

// call sends one command and returns the payload of its successful result.
// Messages that are not this command's response (a stray event, an unrelated
// id) are skipped, because a connection that was opened for a subscription
// would otherwise be unreadable here.
func (w *haWS) call(ctx context.Context, command map[string]any) (json.RawMessage, error) {
	w.nextID++
	id := w.nextID
	payload := make(map[string]any, len(command)+1)
	for k, v := range command {
		payload[k] = v
	}
	payload["id"] = id

	if err := w.write(ctx, payload); err != nil {
		return nil, err
	}
	for {
		if err := w.setDeadline(ctx); err != nil {
			return nil, err
		}
		var msg wsEnvelope
		if err := w.conn.ReadJSON(&msg); err != nil {
			return nil, fmt.Errorf("waiting for the Home Assistant WebSocket answer to %v: %w", command["type"], err)
		}
		if msg.ID == nil || *msg.ID != id {
			continue
		}
		if msg.Type == "result" && msg.Success != nil && *msg.Success {
			return msg.Result, nil
		}
		reason := msg.Message
		if reason == "" {
			reason = msg.Error
		}
		if reason == "" {
			reason = "no reason given"
		}
		code := msg.Code
		if code == "" {
			code = "unknown"
		}
		return nil, fmt.Errorf("the %v command was rejected by Home Assistant: %s (%s)", command["type"], reason, code)
	}
}

// close shuts the connection down with a normal close frame.
func (w *haWS) close() error {
	_ = w.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(2*time.Second),
	)
	return w.conn.Close()
}

// write sends one JSON message under a deadline derived from ctx.
func (w *haWS) write(ctx context.Context, payload map[string]any) error {
	if err := w.setDeadline(ctx); err != nil {
		return err
	}
	if err := w.conn.WriteJSON(payload); err != nil {
		return fmt.Errorf("sending over the Home Assistant WebSocket API: %w", err)
	}
	return nil
}

// setDeadline applies ctx's deadline to the next read or write, defaulting to
// wsHandshakeBudget when the context is open-ended. gorilla's ReadJSON takes no
// context, so the deadline is the only way a stuck connection cannot hold the
// reconciliation pass forever.
func (w *haWS) setDeadline(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(wsHandshakeBudget)
	}
	if err := w.conn.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("setting the Home Assistant WebSocket read deadline: %w", err)
	}
	if err := w.conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("setting the Home Assistant WebSocket write deadline: %w", err)
	}
	return nil
}
