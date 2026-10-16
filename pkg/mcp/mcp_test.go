// SPDX-License-Identifier: AGPL-3.0-only

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/pkg/appclient"
)

// TestProbePresentsTheBearer is the regression guard for a failure a live
// install actually hit: the server came up healthy while every probe was
// refused with 401, because the probe sent no credential and the server
// enforces one. A green node every consumer is about to be refused by is worse
// than a red one.
func TestProbePresentsTheBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\ndata: {\"result\":{\"serverInfo\":{\"name\":\"mcp\",\"version\":\"1\"}},\"jsonrpc\":\"2.0\",\"id\":1}\n\n"))
	}))
	defer srv.Close()

	cl := appclient.New(appclient.Spec{Name: "probe", BaseURL: srv.URL})
	if err := Probe(context.Background(), cl, "/mcp", "the-bearer", 5*time.Second); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if gotAuth != "Bearer the-bearer" {
		t.Fatalf("Authorization = %q, want Bearer the-bearer", gotAuth)
	}
}

// TestProbeSendsNoAuthHeaderWithoutABearer pins the other half: an empty
// bearer sends no header, and the probe fails rather than promoting a server
// that is refusing everything.
func TestProbeSendsNoAuthHeaderWithoutABearer(t *testing.T) {
	var gotAuth string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"Unauthorized"}}`))
	}))
	defer srv.Close()

	cl := appclient.New(appclient.Spec{Name: "probe", BaseURL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := Probe(ctx, cl, "/mcp", "", 2*time.Second); err == nil {
		t.Fatal("Probe without a bearer must fail against a server that refuses it")
	}
	if gotAuth != "" {
		t.Fatalf("Authorization = %q, want empty", gotAuth)
	}
	if calls <= 1 {
		t.Fatalf("calls = %d, want retries", calls)
	}
}
