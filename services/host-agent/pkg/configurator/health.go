package configurator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// DefaultHealthCheckTimeout is the default timeout for health checks
const DefaultHealthCheckTimeout = 60 * time.Second

// WaitForHTTP waits for an HTTP endpoint to return a successful status code.
// It polls the endpoint until it responds with 2xx or the context is cancelled.
func WaitForHTTP(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s: %w", url, ctx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				continue
			}

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()

			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
	}
}

// WaitForHTTPWithAuth waits for an HTTP endpoint that requires authentication.
// It considers 401/403 as "ready" since the service is responding.
func WaitForHTTPWithAuth(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s: %w", url, ctx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				continue
			}

			resp, err := client.Do(req)
			if err != nil {
				continue
			}
			resp.Body.Close()

			// Service is ready if it responds at all (even with auth error)
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				return nil
			}
		}
	}
}

// WaitForTCP waits for a TCP port to accept connections.
func WaitForTCP(ctx context.Context, host string, port int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := fmt.Sprintf("%s:%d", host, port)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for %s: %w", addr, ctx.Err())
		case <-ticker.C:
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
			if err != nil {
				continue
			}
			conn.Close()
			return nil
		}
	}
}

// WaitForSSOReady waits for an app's SSO integration to be ready.
// This should be called in PreStart for any app that has SSO enabled.
// It waits for:
// 1. Authentik to be running
// 2. The Authentik worker to process the app's blueprint
// 3. The OpenID provider/application to be created
// 4. The discovery endpoint to return valid configuration
func WaitForSSOReady(ctx context.Context, appName string, authentikPort int, timeout time.Duration) error {
	url := fmt.Sprintf("http://localhost:%d/application/o/%s/.well-known/openid-configuration", authentikPort, appName)
	return WaitForOpenIDConfig(ctx, url, timeout)
}

// ShouldWaitForSSO checks if an app has SSO configured by probing Authentik.
// Returns true if Authentik responds with 200/502/503 (SSO exists or is starting).
// Returns false if Authentik returns 404 (no such app) or isn't reachable.
// This is useful when you can't check the catalog but need to know if SSO wait is needed.
func ShouldWaitForSSO(appName string, authentikPort int) bool {
	url := fmt.Sprintf("http://localhost:%d/application/o/%s/.well-known/openid-configuration", authentikPort, appName)

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	resp, err := client.Get(url)
	if err != nil {
		// Connection error - Authentik isn't running, no SSO to wait for
		return false
	}
	defer resp.Body.Close()

	// 404 means no OpenID provider for this app - no SSO configured
	if resp.StatusCode == http.StatusNotFound {
		return false
	}

	// 200, 502, 503 suggest SSO is configured (ready or starting)
	return true
}

// WaitForOpenIDConfig waits for an OpenID Connect discovery endpoint to return
// valid configuration. This is more robust than WaitForHTTP because it verifies
// the response contains an "issuer" field, ensuring the OpenID provider/application
// has been fully created (not just that Authentik is responding).
func WaitForOpenIDConfig(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("timeout waiting for OpenID config at %s: %w (last error: %v)", url, ctx.Err(), lastErr)
			}
			return fmt.Errorf("timeout waiting for OpenID config at %s: %w", url, ctx.Err())
		case <-ticker.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				lastErr = err
				continue
			}

			resp, err := client.Do(req)
			if err != nil {
				lastErr = err
				continue
			}

			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()

			if err != nil {
				lastErr = err
				continue
			}

			// Check for successful response
			if resp.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("got status %d", resp.StatusCode)
				continue
			}

			// Verify it's valid OpenID configuration by checking for issuer field
			var config struct {
				Issuer string `json:"issuer"`
			}
			if err := json.Unmarshal(body, &config); err != nil {
				lastErr = fmt.Errorf("invalid JSON: %w", err)
				continue
			}

			if config.Issuer == "" {
				lastErr = fmt.Errorf("missing issuer field")
				continue
			}

			// Valid OpenID configuration found
			return nil
		}
	}
}
