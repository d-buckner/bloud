package configurator

import (
	"context"
	"fmt"
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
