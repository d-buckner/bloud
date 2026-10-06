// SPDX-License-Identifier: AGPL-3.0-only

package appclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A readiness wait against a server-sent-events stream must return the moment
// the answer arrives, not when the server closes the stream. supergateway's
// streamable HTTP keeps the stream open after the response for server-initiated
// notifications, so a read-to-EOF would sit on the per-request deadline even
// though the answer was in the first frames. This server flushes the answer and
// then holds the connection open longer than the request's own deadline.
func TestReadyWaitStopsAtStreamedResultWithoutEOF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			": keepalive\n\nevent: message\n" +
				"data: {\"result\":{\"serverInfo\":{\"name\":\"dav-mcp\"}},\"jsonrpc\":\"2.0\",\"id\":1}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(5 * time.Second)
	}))
	defer srv.Close()

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	start := time.Now()
	err := c.POST("/mcp").
		Body([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`), "application/json").
		Header("Accept", "application/json, text/event-stream").
		Timeout(2 * time.Second). // shorter than the stream the server holds open
		Ready(func(status int, body []byte) bool {
			return status == http.StatusOK && strings.Contains(string(body), `"name":"dav-mcp"`)
		}).
		Wait(context.Background())

	require.NoError(t, err)
	assert.Less(t, time.Since(start), 2*time.Second,
		"the wait must return when the answer lands, not when the stream closes")
}

// A stream that stays open but never carries the answer is still "not ready":
// the predicate keeps failing and the wait runs out its budget rather than
// mistaking keepalives for a result.
func TestReadyWaitKeepalivesAloneAreNotReady(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			for i := 0; i < 3; i++ {
				_, _ = w.Write([]byte(": keepalive\n\n"))
				f.Flush()
				time.Sleep(30 * time.Millisecond)
			}
		}
	}))
	defer srv.Close()

	c := New(Spec{Name: "test", BaseURL: srv.URL})
	c.WithSleeper(func(time.Duration) {})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := c.POST("/mcp").
		Body([]byte(`{}`), "application/json").
		Header("Accept", "application/json, text/event-stream").
		Ready(func(status int, body []byte) bool {
			return strings.Contains(string(body), `"name":"dav-mcp"`)
		}).
		Wait(ctx)

	require.Error(t, err, "keepalives without a result must not read as ready")
}
