// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package appflowy configures the AppFlowy stack: it writes the nginx reverse
// proxy config before the stack starts (PreStart) and verifies that every
// proxied route (web, cloud API, GoTrue auth) answers after startup
// (PostStart).
package appflowy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const containerName = "apps-appflowy-nginx"

// configFileName is the nginx config written in PreStart and mounted into the
// nginx container at /etc/nginx/nginx.conf.
const configFileName = "nginx.conf"

// Configurator handles AppFlowy configuration. It is registered under the
// nginx container name (the stack's entry point, which starts last), so
// PreStart runs right before the nginx container is created and PostStart
// runs once the whole stack is healthy. When sso is enabled, PostStart also
// wires Bloud SSO into the stack (Authentik app + GoTrue OIDC provider);
// see sso.go for the design.
type Configurator struct {
	port   int
	sso    SSOConfig
	logger *slog.Logger
}

// NewConfigurator creates a new AppFlowy configurator. port is the
// host-published port of the stack's nginx container. sso carries the Bloud
// SSO wiring inputs (zero value = local sign-up only).
func NewConfigurator(port int, sso SSOConfig, logger *slog.Logger) *Configurator {
	return &Configurator{port: port, sso: sso, logger: logger}
}

func (c *Configurator) Name() string {
	return containerName
}

// nginxConf is the reverse proxy config for the AppFlowy stack. It is modeled
// on the official AppFlowy-Cloud nginx/nginx.conf (self-hosted setup), without
// TLS and without the optional admin/pgadmin/console routes (Bloud does not
// run those). Upstreams are the fixed Bloud container names, resolved by nginx
// at startup via the podman network DNS.
//
// Path map (browser -> service):
//
//	/            -> appflowy_web (SPA + SSR)
//	/api         -> appflowy_cloud (REST + realtime)
//	/ws          -> appflowy_cloud (WebSocket, upgraded)
//	/gotrue/*    -> gotrue (auth; prefix stripped)
//	/minio-api/* -> minio (presigned object URLs; Host rewritten to the
//	                   internal name so SigV4 signatures stay valid)
const nginxConf = `# AppFlowy reverse proxy for Bloud.
# Written by the AppFlowy configurator (PreStart). See apps/appflowy/INTEGRATION.md.

events {
    worker_connections 1024;
}

http {
    map $http_upgrade $connection_upgrade {
        default upgrade;
        '' close;
    }

    server {
        listen 80;
        client_max_body_size 2G;
        underscores_in_headers on;

        set $minio_internal_host "apps-appflowy-minio:9000";

        # Health check (proxied to the web container's nginx).
        location = /health {
            proxy_pass http://apps-appflowy-web:80;
            proxy_set_header Host $host;
        }

        # GoTrue (auth): strip the /gotrue prefix before forwarding.
        location /gotrue/ {
            proxy_pass http://apps-appflowy-gotrue:9999;
            rewrite ^/gotrue(/.*)$ $1 break;
            proxy_set_header Host $http_host;
            proxy_pass_request_headers on;
        }

        # WebSocket endpoint (realtime collaboration).
        location /ws {
            proxy_pass http://apps-appflowy-cloud:8000;
            proxy_http_version 1.1;
            proxy_set_header Upgrade $http_upgrade;
            proxy_set_header Connection "Upgrade";
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_read_timeout 86400s;
        }

        # AppFlowy Cloud API.
        location /api {
            proxy_pass http://apps-appflowy-cloud:8000;
            proxy_set_header X-Request-Id $request_id;
            proxy_set_header Host $http_host;

            # Chat streams long-running responses.
            location /api/chat {
                proxy_pass http://apps-appflowy-cloud:8000;
                proxy_http_version 1.1;
                proxy_set_header Connection "";
                chunked_transfer_encoding on;
                proxy_buffering off;
                proxy_cache off;
                proxy_read_timeout 600s;
                proxy_connect_timeout 600s;
                proxy_send_timeout 600s;
            }

            # Document imports can be large and slow.
            location /api/import {
                proxy_pass http://apps-appflowy-cloud:8000;
                proxy_set_header X-Request-Id $request_id;
                proxy_set_header Host $http_host;
                proxy_read_timeout 600s;
                proxy_connect_timeout 600s;
                proxy_send_timeout 600s;
                proxy_request_buffering off;
                proxy_buffering off;
                proxy_cache off;
            }
        }

        # Presigned object URLs (MinIO). The Host header must be the internal
        # name, because presigned URLs are signed against it (SigV4).
        location /minio-api/ {
            proxy_pass http://apps-appflowy-minio:9000;
            proxy_set_header Host $minio_internal_host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            rewrite ^/minio-api/(.*) /$1 break;
            proxy_connect_timeout 300s;
            proxy_read_timeout 600s;
            proxy_send_timeout 600s;
            proxy_request_buffering off;
            proxy_http_version 1.1;
            proxy_set_header Connection "";
            chunked_transfer_encoding off;
            client_max_body_size 0;
        }

        # AppFlowy Web (SPA + SSR) - default route.
        location / {
            proxy_pass http://apps-appflowy-web:80;
            proxy_set_header X-Scheme $scheme;
            proxy_set_header Host $host;
        }
    }
}
`

// PreStart writes the nginx config before the nginx container starts.
// Returns changed=true when the file content changed, so the orchestrator
// recreates the nginx container to pick up the new config.
func (c *Configurator) PreStart(_ context.Context, state *configurator.AppState) (bool, error) {
	configDir := filepath.Join(state.DataPath, "config")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return false, fmt.Errorf("creating config dir: %w", err)
	}

	path := filepath.Join(configDir, configFileName)
	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, []byte(nginxConf)) {
		// The nginx worker reads the file as an unprivileged user through
		// the "other" permission bit; skip the rewrite when that is set.
		if st, statErr := os.Stat(path); statErr == nil && st.Mode().Perm()&0004 != 0 {
			return false, nil
		}
	}

	// Mode 0644: the nginx worker runs as an unprivileged user and reads the
	// bind-mounted file through the "other" permission bits.
	if err := os.WriteFile(path, []byte(nginxConf), 0644); err != nil {
		return false, fmt.Errorf("writing %s: %w", configFileName, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		return false, fmt.Errorf("setting mode on %s: %w", configFileName, err)
	}
	return true, nil
}
func (c *Configurator) Remove(_ context.Context, _ *configurator.AppState, _ bool) error {
	return nil
}

// PostStart verifies the proxied routes end to end: the web frontend, the
// cloud API, and the GoTrue auth service must all answer through nginx.
// The container health checks already gate convergence; this catches a
// misrouted or partially wired stack before the app is promoted to RUNNING.
// Then it wires Bloud SSO (best-effort — failures are logged and retried
// next cycle, never blocking the app).
func (c *Configurator) PostStart(ctx context.Context, _ *configurator.AppState) error {
	if err := c.verifyRoutes(ctx); err != nil {
		return err
	}
	c.configureSSO(ctx)
	return nil
}

// verifyRoutes polls the proxied routes until they answer (or the deadline
// passes).
func (c *Configurator) verifyRoutes(ctx context.Context) error {
	routes := []struct {
		path string
		want int
	}{
		{"/health", http.StatusOK},        // nginx -> web
		{"/api/health", http.StatusOK},    // nginx -> cloud
		{"/gotrue/health", http.StatusOK}, // nginx -> gotrue
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	client := &http.Client{Timeout: 10 * time.Second}

	for _, r := range routes {
		if err := c.waitForRoute(ctx, client, r.path, r.want); err != nil {
			return err
		}
	}
	c.logger.Info("appflowy routes verified", "routes", len(routes))
	return nil
}

// waitForRoute polls the given path on the published port until it returns
// the expected status or the context expires (cancellation or deadline).
func (c *Configurator) waitForRoute(ctx context.Context, client *http.Client, path string, want int) error {
	url := c.baseURL() + path
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return fmt.Errorf("building request for %s: %w", path, err)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == want {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
			c.logger.Warn("appflowy route not ready yet", "path", path, "status", resp.StatusCode, "body", string(body))
		} else if ctx.Err() == nil {
			lastErr = err
			c.logger.Warn("appflowy route unreachable", "path", path, "error", err)
		}

		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("appflowy route %s did not become ready: %w", path, lastErr)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Configurator) baseURL() string {
	return fmt.Sprintf("http://localhost:%d", c.port)
}
