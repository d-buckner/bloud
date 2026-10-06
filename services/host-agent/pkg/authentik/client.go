// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Client provides access to the Authentik API. It wraps a single
// *appclient.Client: one appclient spec carries the base URL, the
// management-token bearer, the Accept header, and the (single-shot)
// retry/timeout policy, so every method below reads as declared intent
// over a shared transport instead of hand-rolling http.NewRequest.
type Client struct {
	baseURL     string
	cl          *appclient.Client
	emailDomain string
	spec        appclient.Spec
}

// UserEmailDomain returns the domain used for managed users' identity
// emails. SSO apps validate identity emails with an RFC-style validator
// that requires a TLD, so the bare "localhost" base domain used in dev
// environments is mapped to "localhost.local".
func UserEmailDomain(baseDomain string) string {
	if baseDomain == "" || baseDomain == "localhost" {
		return "localhost.local"
	}
	return baseDomain
}

// NewClient creates a new Authentik API client.
//
// The underlying appclient is single-shot (one attempt, no backoff) and
// 30s per-request, matching the client's historical behavior: these calls
// run inside an idempotent reconciliation pass, so a transient blip is
// retried on the next cycle rather than backed off here.
func NewClient(baseURL, token string) *Client {
	c := &Client{
		baseURL:     baseURL,
		emailDomain: UserEmailDomain(""),
	}
	c.spec = appclient.Spec{
		Name:    "authentik",
		BaseURL: baseURL,
		Headers: map[string]string{"Accept": "application/json"},
		StaticAuth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+token)
		},
		Timeout: 30 * time.Second,
		Retry:   appclient.RetryPolicy{MaxAttempts: 1},
	}
	c.cl = appclient.New(c.spec)
	return c
}

// WithClientFactory stamps the process-shared transport and logger into the
// client's appclient spec, so the Authentik client reuses the host's connection
// pool instead of owning one. Fields already set on the spec (retry, timeout,
// auth) win over the factory defaults.
func (c *Client) WithClientFactory(f configurator.ClientFactory) *Client {
	c.cl = f.New(c.spec)
	return c
}

// WithUserEmailDomain sets the domain used for managed users' identity
// emails (see UserEmailDomain). Defaults to "localhost.local".
func (c *Client) WithUserEmailDomain(domain string) *Client {
	c.emailDomain = UserEmailDomain(domain)
	return c
}

// ManagedUserEmail returns the identity email for a managed user.
func (c *Client) ManagedUserEmail(username string) string {
	return username + "@" + c.emailDomain
}

// ProviderResponse represents an Authentik API response
type ProviderResponse struct {
	PK   int    `json:"pk"`
	Name string `json:"name"`
	// AssignedApplicationSlug is the slug of the application this provider is
	// attached to, empty when it is attached to none. Bloud always links a
	// per-app provider to the per-app application, which makes this the
	// stable key for uninstall cleanup: the provider's *name* embeds the
	// display name, the application link embeds the catalog ID, and only one
	// of those two can never change.
	AssignedApplicationSlug string `json:"assigned_application_slug"`
}

// PaginatedResponse represents a paginated Authentik API response
type PaginatedResponse struct {
	Pagination struct {
		Count int `json:"count"`
	} `json:"pagination"`
	Results []ProviderResponse `json:"results"`
}

// IsAvailable checks if Authentik is available and the token is valid.
//
// A nil client reports unavailable. Callers hold this type behind an interface
// (api.AuthentikUserManagerInterface), where a nil *Client is not a nil
// interface, so the guard must live here rather than only at call sites.
func (c *Client) IsAvailable(ctx context.Context) bool {
	if c == nil || c.cl == nil {
		return false
	}
	_, err := c.cl.GET("/api/v3/core/applications/").Do(ctx)
	return err == nil
}

// createBloudApplication creates the Authentik application for Bloud
func (c *Client) createBloudApplication(ctx context.Context, providerID int) error {
	payload := map[string]any{
		"name":               bloudAppName,
		"slug":               bloudAppSlug,
		"provider":           providerID,
		"policy_engine_mode": "any",
	}
	if err := c.cl.POST("/api/v3/core/applications/").JSON(payload).OK(http.StatusCreated).Exec(ctx); err != nil {
		return fmt.Errorf("creating application: %w", err)
	}
	return nil
}

// sleepCtx sleeps for d unless ctx is done first; returns the ctx error when
// canceled. Used by the login/branding retry loops so a PostStart-budget
// cancel interrupts the wait instead of blocking for the full interval.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
