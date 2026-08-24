// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testFrontProxy wires a frontProxy against two httptest backends: the
// host-agent health endpoint and the Traefik target.
func testFrontProxy(t *testing.T, agentReady bool) (*frontProxy, *httptest.Server) {
	t.Helper()

	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		if agentReady {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		} else {
			http.Error(w, "down", http.StatusServiceUnavailable)
		}
	}))
	t.Cleanup(agent.Close)

	traefik := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			w.Write([]byte("ok"))
			return
		}
		w.Header().Set("X-Test", "proxied")
		_, _ = w.Write([]byte("traefik-body:" + r.Host))
	}))
	t.Cleanup(traefik.Close)

	f, err := newFrontProxy(slog.Default())
	if err != nil {
		t.Fatalf("newFrontProxy: %v", err)
	}
	f.agentHealthURL = agent.URL + "/api/health"
	f.traefikAddr = strings.TrimPrefix(traefik.URL, "http://")
	// Rebuild the proxy with the test backend (newFrontProxy targets :8080).
	f.proxy.Director = func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = f.traefikAddr
	}
	return f, agent
}

func TestFrontProxyServesFallbackWhenAgentDown(t *testing.T) {
	f, _ := testFrontProxy(t, false)

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://bloud.local/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("fallback status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Bloud") || !strings.Contains(body, "starting up") {
		t.Errorf("fallback page missing branding/copy: %q", body)
	}
	if !strings.Contains(body, `http-equiv="refresh"`) {
		t.Errorf("fallback page missing auto-reload: %q", body)
	}
}

func TestFrontProxyServesProvisioningCopyWhenTraefikUp(t *testing.T) {
	f, _ := testFrontProxy(t, false)

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://bloud.local/", nil))

	// traefikUp probe hits the test traefik's /ping, which answers OK.
	if !strings.Contains(rec.Body.String(), "provisioning system services") {
		t.Errorf("expected provisioning copy when traefik is up, got: %q", rec.Body.String())
	}
}

func TestFrontProxyForwardsWhenAgentReady(t *testing.T) {
	f, _ := testFrontProxy(t, true)

	req := httptest.NewRequest(http.MethodGet, "http://jellyfin.bloud.local/media", nil)
	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Header().Get("X-Test") != "proxied" {
		t.Fatal("response was not proxied from the traefik backend")
	}
	// The original Host header must survive the proxy so Traefik's
	// HostRegexp routing keeps working.
	if body := rec.Body.String(); !strings.Contains(body, "jellyfin.bloud.local") {
		t.Errorf("Host header not preserved through proxy, body: %q", body)
	}
}

func TestFrontProxyAgentReadyCaches(t *testing.T) {
	calls := 0
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer agent.Close()

	f, err := newFrontProxy(slog.Default())
	if err != nil {
		t.Fatalf("newFrontProxy: %v", err)
	}
	f.agentHealthURL = agent.URL + "/api/health"

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if !f.agentReady(ctx) {
			t.Fatal("agentReady = false, want true")
		}
	}
	if calls != 1 {
		t.Errorf("health endpoint called %d times, want 1 (cached)", calls)
	}
}

func TestFrontProxyServesFallbackAfterProxyError(t *testing.T) {
	f, _ := testFrontProxy(t, true)
	// Point the proxy at a dead backend: the ErrorHandler must serve the
	// fallback page instead of failing the request.
	f.proxy.Director = func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = "127.0.0.1:1"
	}

	rec := httptest.NewRecorder()
	f.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://bloud.local/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fallback page)", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "Bloud") {
		t.Errorf("expected fallback page after proxy error, got: %q", body)
	}
}
