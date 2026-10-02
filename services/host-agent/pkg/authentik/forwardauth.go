// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

// EnsureForwardAuthApplication creates or verifies the Authentik proxy provider and
// application for an app using the forward-auth SSO strategy. It also adds the
// provider to the embedded outpost so Traefik's forwardAuth middleware can reach it.
// externalURL is the full URL users access the app on, e.g. "http://navidrome.localhost:8080".
func (c *Client) EnsureForwardAuthApplication(ctx context.Context, appName, displayName, externalURL string) error {
	providerName := fmt.Sprintf("%s Proxy Provider", displayName)

	// The outpost matches a provider by the X-Forwarded-Host and scheme Traefik
	// forwards, so the readiness probe below must present the host this URL
	// states. Parse it once up front and fail a malformed URL before any
	// provisioning work.
	gateURL, err := url.Parse(externalURL)
	if err != nil {
		return fmt.Errorf("parsing forward-auth external URL: %w", err)
	}

	// Check if provider already exists
	existingID, err := c.findProviderID(ctx, "proxy", providerName)
	if err != nil {
		return fmt.Errorf("checking proxy provider: %w", err)
	}

	var providerID int
	if existingID != 0 {
		providerID = existingID
	} else {
		// Find required flows
		authFlowID, err := c.findFlowID(ctx, "default-authentication-flow")
		if err != nil {
			return fmt.Errorf("finding auth flow: %w", err)
		}
		invalidationFlowID, err := c.findFlowID(ctx, "default-provider-invalidation-flow")
		if err != nil {
			return fmt.Errorf("finding invalidation flow: %w", err)
		}

		providerID, err = c.createProxyProvider(ctx, providerName, externalURL, authFlowID, invalidationFlowID)
		if err != nil {
			return fmt.Errorf("creating proxy provider: %w", err)
		}
	}

	// Ensure application exists
	if err := c.ensureProxyApplication(ctx, appName, displayName, providerID); err != nil {
		return fmt.Errorf("ensuring proxy application: %w", err)
	}

	// Add provider to embedded outpost
	if err := c.AddProviderToEmbeddedOutpost(ctx, providerName); err != nil {
		return fmt.Errorf("adding to embedded outpost: %w", err)
	}

	// The embedded outpost reloads its provider list asynchronously after the
	// PATCH that adds it, so a freshly-added app's forward-auth gate can answer
	// "Not Found" for a short window. Wait until the outpost recognizes the
	// provider so the app is not marked RUNNING while its gate 404s.
	if err := c.waitForwardAuthProviderReady(ctx, gateURL.Host, gateURL.Scheme); err != nil {
		return fmt.Errorf("waiting for the outpost to recognize the provider: %w", err)
	}

	return nil
}

// forwardAuthProbeBudget bounds how long a freshly-added forward-auth provider
// may take to be recognized by the embedded outpost. The reload is asynchronous
// and can lag the app's own convergence by tens of seconds on a loaded host.
const forwardAuthProbeBudget = 90 * time.Second

// waitForwardAuthProviderReady polls the embedded outpost's forward-auth
// endpoint (the same address Traefik's forwardAuth middleware calls) until it
// recognizes the provider for host. An unauthenticated request to a recognized
// provider is answered with a redirect to the flow; an unrecognized one is
// answered with the outpost's own "Not Found" page. This is the barrier that
// closes the race between adding a provider and the outpost loading it.
func (c *Client) waitForwardAuthProviderReady(ctx context.Context, host, scheme string) error {
	// A dedicated client that observes redirects instead of following them: the
	// probe's whole signal is the outpost's 302 (recognized) versus its 404
	// "Not Found" (provider not loaded yet), and the shared API client follows
	// redirects, which would chase the 302 off the outpost host.
	noFollow := false
	probe := appclient.New(appclient.Spec{
		Name:            "authentik-outpost",
		BaseURL:         c.baseURL,
		FollowRedirects: &noFollow,
	})
	return probe.GET("/outpost.goauthentik.io/auth/traefik").
		Header("X-Forwarded-Host", host).
		Header("X-Forwarded-Proto", scheme).
		Header("X-Forwarded-Method", http.MethodGet).
		Header("X-Forwarded-Uri", "/").
		OK(http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect).
		Ready(appclient.StatusIn(http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect)).
		Interval(2 * time.Second).
		Within(forwardAuthProbeBudget).
		Wait(ctx)
}

// createProxyProvider creates a new Authentik proxy provider in forward_single mode.
func (c *Client) createProxyProvider(ctx context.Context, name, externalHost, authFlowID, invalidationFlowID string) (int, error) {
	payload := map[string]interface{}{
		"name":               name,
		"authorization_flow": authFlowID,
		"invalidation_flow":  invalidationFlowID,
		"external_host":      externalHost,
		"mode":               "forward_single",
	}
	var result struct {
		PK int `json:"pk"`
	}
	if err := c.cl.POST("/api/v3/providers/proxy/").JSON(payload).OK(http.StatusCreated).DoInto(ctx, &result); err != nil {
		return 0, err
	}
	return result.PK, nil
}

// ensureProxyApplication creates the Authentik application for a proxy provider if it doesn't exist.
func (c *Client) ensureProxyApplication(ctx context.Context, slug, displayName string, providerID int) error {
	appPath := "/api/v3/core/applications/" + url.PathEscape(slug) + "/"
	_, err := c.cl.GET(appPath).Do(ctx)
	if err == nil {
		return nil // Already exists
	}
	if appclient.StatusOf(err) == 0 {
		return err // transport error: don't attempt create on an unreachable server
	}

	// Create application
	payload := map[string]interface{}{
		"name":               displayName,
		"slug":               slug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return err
	}
	return nil
}
