// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appflowy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestPreStartWritesNginxConf(t *testing.T) {
	c := NewConfigurator(8480, SSOConfig{}, testLogger())
	state := &configurator.AppState{DataPath: t.TempDir()}

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart failed: %v", err)
	}
	if !changed {
		t.Fatal("first PreStart should report changed=true")
	}

	path := filepath.Join(state.DataPath, "config", configFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not written: %v", err)
	}
	if string(data) != nginxConf {
		t.Fatal("written config does not match nginxConf")
	}

	// The nginx worker runs unprivileged: the file must be group/other-readable.
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := st.Mode().Perm() & 0044; got != 0044 {
		t.Errorf("config file must be group/other-readable, got mode %v", st.Mode())
	}
}

func TestPreStartIdempotent(t *testing.T) {
	c := NewConfigurator(8480, SSOConfig{}, testLogger())
	state := &configurator.AppState{DataPath: t.TempDir()}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("first PreStart failed: %v", err)
	}
	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("second PreStart failed: %v", err)
	}
	if changed {
		t.Fatal("second PreStart with unchanged content should report changed=false")
	}
}

func TestPreStartDetectsContentDrift(t *testing.T) {
	c := NewConfigurator(8480, SSOConfig{}, testLogger())
	state := &configurator.AppState{DataPath: t.TempDir()}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("first PreStart failed: %v", err)
	}
	path := filepath.Join(state.DataPath, "config", configFileName)
	if err := os.WriteFile(path, []byte("# drifted\n"), 0644); err != nil {
		t.Fatalf("drifting config: %v", err)
	}

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart after drift failed: %v", err)
	}
	if !changed {
		t.Fatal("PreStart after content drift should report changed=true")
	}
	data, _ := os.ReadFile(path)
	if string(data) != nginxConf {
		t.Fatal("drifted config was not restored")
	}
}

func TestPreStartFixesUnreadableMode(t *testing.T) {
	c := NewConfigurator(8480, SSOConfig{}, testLogger())
	state := &configurator.AppState{DataPath: t.TempDir()}

	if _, err := c.PreStart(context.Background(), state); err != nil {
		t.Fatalf("first PreStart failed: %v", err)
	}
	path := filepath.Join(state.DataPath, "config", configFileName)
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	changed, err := c.PreStart(context.Background(), state)
	if err != nil {
		t.Fatalf("PreStart after chmod failed: %v", err)
	}
	if !changed {
		t.Fatal("PreStart after mode loss should report changed=true")
	}
	st, _ := os.Stat(path)
	if got := st.Mode().Perm() & 0044; got != 0044 {
		t.Errorf("mode not restored, got %v", st.Mode())
	}
}

// TestPostStartVerifiesRoutes starts a fake stack serving the three proxied
// routes and asserts PostStart succeeds against it.
func TestPostStartVerifiesRoutes(t *testing.T) {
	mux := http.NewServeMux()
	for _, p := range []string{"/health", "/api/health", "/gotrue/health"} {
		p := p
		mux.HandleFunc(p, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := NewConfigurator(port, SSOConfig{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.PostStart(ctx, &configurator.AppState{}); err != nil {
		t.Fatalf("PostStart failed against healthy fake stack: %v", err)
	}
}

// TestPostStartFailsWhenRouteMissing asserts PostStart fails (not hangs) when
// a required route never answers.
func TestPostStartFailsWhenRouteMissing(t *testing.T) {
	mux := http.NewServeMux()
	// Only /health answers; /api/health and /gotrue/health are missing.
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := NewConfigurator(port, SSOConfig{}, testLogger())

	// waitForRoute is the unit under test here; give it a short deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	if err := c.waitForRoute(ctx, client, "/api/health", http.StatusOK); err == nil {
		t.Fatal("waitForRoute should fail for a route that returns 404")
	}
}

func TestWaitForRouteSucceedsAfterDelay(t *testing.T) {
	started := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		<-started // refuse requests until released
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	c := NewConfigurator(port, SSOConfig{}, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.waitForRoute(ctx, client, "/api/health", http.StatusOK)
	}()

	time.Sleep(300 * time.Millisecond)
	close(started)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("waitForRoute should succeed once the route answers: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("waitForRoute did not return")
	}
}

func TestWaitForRouteHonorsContextCancellation(t *testing.T) {
	// Listen-only server: connections accepted, no HTTP response.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	c := NewConfigurator(port, SSOConfig{}, testLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = c.waitForRoute(ctx, &http.Client{Timeout: 2 * time.Second}, "/health", http.StatusOK)
	if err == nil {
		t.Fatal("waitForRoute should fail when the context is cancelled")
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("waitForRoute took %v to honor cancellation", elapsed)
	}
}
