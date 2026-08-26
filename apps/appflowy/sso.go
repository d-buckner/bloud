// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appflowy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
)

// Bloud SSO wiring for AppFlowy's GoTrue auth layer.
//
// AppFlowy's cloud API only accepts JWTs minted by its bundled GoTrue, so
// Bloud SSO cannot be a proxy-level concern (forward-auth): it lives inside
// GoTrue as an admin-registered custom OIDC provider ("Sign in with Bloud").
// Bloud owns the whole wiring and re-applies it on every reconcile cycle
// (PostStart), so the relationship self-heals:
//
//  1. The Authentik OAuth2 application for AppFlowy (per-app provider +
//     application, the same pattern as the native-oidc apps), with the
//     GoTrue callback URL on the public origin as its redirect URI.
//  2. The custom OIDC provider in GoTrue (identifier "custom:bloud-sso"),
//     pointing at the Bloud SSO issuer over TLS.
//
// GoTrue validates the issuer at registration: it rejects literal
// localhost/loopback hosts and any hostname resolving to a loopback or
// private address (verified against gotrue 0.17.12). Local dev deployments
// (sso.localhost) are therefore skipped with an info log; real deployments
// with a public FQDN register normally. SSO wiring failures are logged and
// retried on the next cycle — they never block the app from starting (local
// email/password sign-up remains available either way).

const (
	// ssoProviderIdentifier is the GoTrue custom provider identifier. The
	// "custom:" prefix is required by the GoTrue admin API.
	ssoProviderIdentifier = "custom:bloud-sso"
	// ssoProviderName is the display name rendered as the login button.
	ssoProviderName = "Bloud SSO"
)

// SSOConfig carries the inputs for wiring AppFlowy's GoTrue to Bloud SSO.
// A zero value disables SSO wiring (local sign-up only).
type SSOConfig struct {
	IssuerURL           string // e.g. https://sso.example.com/application/o/appflowy/
	RedirectURI         string // GoTrue callback on the public origin
	LaunchURL           string // AppFlowy public URL (Authentik launch URL)
	ClientID            string // OAuth2 client ID (deterministic per app)
	ClientSecret        string // OAuth2 client secret (derived from the host secret)
	GotrueAdminEmail    string // GoTrue admin user (supabase_admin role)
	GotrueAdminPassword string
	AuthentikBaseURL    string // Authentik API base (direct port)
	AuthentikToken      string // Authentik API token
}

// Enabled reports whether the SSO wiring inputs are complete.
func (s SSOConfig) Enabled() bool {
	return s.IssuerURL != "" && s.RedirectURI != "" &&
		s.ClientID != "" && s.ClientSecret != "" &&
		s.GotrueAdminEmail != "" && s.GotrueAdminPassword != ""
}

// IssuerRegistrable reports whether the issuer host can be registered with
// GoTrue's custom provider API. GoTrue rejects literal localhost/loopback
// hosts and hostnames resolving to loopback or private addresses, so local
// dev deployments (sso.localhost, 127.0.0.1) cannot register; public FQDNs
// (and public IP literals) can.
func IssuerRegistrable(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsPrivate()
	}
	return true
}

// configureSSO wires Bloud SSO into the running stack. Best-effort by
// design: any failure is logged and retried on the next reconcile cycle,
// never blocking the app (local sign-up stays available).
func (c *Configurator) configureSSO(ctx context.Context) {
	if !c.sso.Enabled() {
		return
	}
	if !IssuerRegistrable(c.sso.IssuerURL) {
		c.logger.Info("appflowy SSO skipped: GoTrue rejects local issuers (localhost/loopback/private); local sign-up remains available",
			"issuer", c.sso.IssuerURL)
		return
	}

	if err := c.ensureAuthentikApp(ctx); err != nil {
		c.logger.Warn("appflowy SSO: ensuring Authentik application failed; retrying next cycle", "error", err)
		return
	}
	if err := c.ensureGotrueProvider(ctx); err != nil {
		level := slog.LevelWarn
		if isGoTrueValidationRejection(err) {
			// A 400 validation rejection is an environment constraint
			// (e.g. the issuer resolves to a private address on this
			// network), not a transient failure.
			level = slog.LevelInfo
		}
		c.logger.Log(ctx, level, "appflowy SSO: GoTrue provider registration failed; retrying next cycle", "error", err)
		return
	}
	c.logger.Info("appflowy SSO wired to Bloud", "issuer", c.sso.IssuerURL, "provider", ssoProviderIdentifier)
}

// ensureAuthentikApp creates or verifies the Authentik OAuth2 provider and
// application for AppFlowy. Idempotent: an existing provider has its
// redirect URIs refreshed (self-healing across public-URL changes).
func (c *Configurator) ensureAuthentikApp(ctx context.Context) error {
	client := authentik.NewClient(c.sso.AuthentikBaseURL, c.sso.AuthentikToken)
	return client.EnsureNativeOIDC("appflowy", "AppFlowy",
		c.sso.ClientID, c.sso.ClientSecret,
		[]string{c.sso.RedirectURI}, c.sso.LaunchURL)
}

// customProvider is the GoTrue admin-API view of a custom OAuth provider.
// client_secret is intentionally absent: the API never returns it.
type customProvider struct {
	ProviderType     string            `json:"provider_type"`
	Identifier       string            `json:"identifier"`
	Name             string            `json:"name"`
	ClientID         string            `json:"client_id"`
	Scopes           []string          `json:"scopes"`
	AttributeMapping map[string]string `json:"attribute_mapping"`
	Issuer           string            `json:"issuer"`
}

// desiredProvider is the provider configuration Bloud wants in GoTrue.
// Accounts are keyed by the Authentik email, so the mapping is the single
// email claim; GOTRUE_MAILER_AUTOCONFIRM=true means no confirmation step.
func (s SSOConfig) desiredProvider() customProvider {
	return customProvider{
		ProviderType:     "oidc",
		Identifier:       ssoProviderIdentifier,
		Name:             ssoProviderName,
		ClientID:         s.ClientID,
		Scopes:           []string{"openid", "email", "profile"},
		AttributeMapping: map[string]string{"email": "{claims.email}"},
		Issuer:           s.IssuerURL,
	}
}

// providerMatches reports whether an existing provider carries the desired
// configuration (client_secret is not comparable: the API omits it).
func (s SSOConfig) providerMatches(p customProvider) bool {
	want := s.desiredProvider()
	if p.ProviderType != want.ProviderType || p.Name != want.Name ||
		p.Issuer != want.Issuer || p.ClientID != want.ClientID {
		return false
	}
	if !equalStringSets(p.Scopes, want.Scopes) {
		return false
	}
	if len(p.AttributeMapping) != len(want.AttributeMapping) {
		return false
	}
	for k, v := range want.AttributeMapping {
		if p.AttributeMapping[k] != v {
			return false
		}
	}
	return true
}

func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// ensureGotrueProvider registers (or re-registers on drift) the Bloud SSO
// custom OIDC provider in GoTrue. Idempotent by identifier — the identifier
// is unique in GoTrue — so an existing provider is compared and only
// replaced (DELETE + POST, the API's only verified mutation path) when its
// configuration drifted.
func (c *Configurator) ensureGotrueProvider(ctx context.Context) error {
	token, err := c.gotrueAdminToken(ctx)
	if err != nil {
		return fmt.Errorf("obtaining GoTrue admin token: %w", err)
	}
	base := c.baseURL() + "/gotrue"

	providers, err := c.gotrueListProviders(ctx, base, token)
	if err != nil {
		return err
	}
	for _, p := range providers {
		if p.Identifier != ssoProviderIdentifier {
			continue
		}
		if c.sso.providerMatches(p) {
			return nil // already correct
		}
		if err := c.gotrueDeleteProvider(ctx, base, token); err != nil {
			return fmt.Errorf("replacing drifted provider: %w", err)
		}
		break
	}
	return c.gotrueCreateProvider(ctx, base, token)
}

// gotrueAdminToken authenticates the GoTrue admin (password grant). This
// fork's token endpoint takes a JSON body, not the standard form grant.
func (c *Configurator) gotrueAdminToken(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"email":    c.sso.GotrueAdminEmail,
		"password": c.sso.GotrueAdminPassword,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL()+"/gotrue/token?grant_type=password", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	data, err := readGoTrueBody(resp)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gotrue admin login: status %d: %s", resp.StatusCode, truncate(data))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", fmt.Errorf("parsing gotrue admin login: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("gotrue admin login returned no access_token")
	}
	return out.AccessToken, nil
}

// gotrueError carries a GoTrue API failure so callers can classify it
// (validation rejection vs transient).
type gotrueError struct {
	status int
	msg    string
}

func (e *gotrueError) Error() string {
	return fmt.Sprintf("gotrue API: status %d: %s", e.status, e.msg)
}

// isGoTrueValidationRejection reports whether the error is a GoTrue-side
// 400 validation rejection: an environment constraint (e.g. a local issuer
// GoTrue refuses) rather than a transient failure.
func isGoTrueValidationRejection(err error) bool {
	var ge *gotrueError
	return errors.As(err, &ge) && ge.status == http.StatusBadRequest
}

func (c *Configurator) gotrueRequest(ctx context.Context, method, reqURL, token string, payload any) (*http.Response, []byte, error) {
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, reader)
	if err != nil {
		return nil, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, nil, err
	}
	data, err := readGoTrueBody(resp)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}

func readGoTrueBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// gotrueFailure builds a *gotrueError from a non-success response, parsing
// the GoTrue error envelope when present.
func gotrueFailure(status int, data []byte) error {
	var env struct {
		ErrorCode string `json:"error_code"`
		Msg       string `json:"msg"`
	}
	if err := json.Unmarshal(data, &env); err == nil && (env.ErrorCode != "" || env.Msg != "") {
		return &gotrueError{status: status, msg: env.Msg}
	}
	return &gotrueError{status: status, msg: truncate(data)}
}

func truncate(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

func (c *Configurator) gotrueListProviders(ctx context.Context, base, token string) ([]customProvider, error) {
	resp, data, err := c.gotrueRequest(ctx, http.MethodGet, base+"/admin/custom-providers", token, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, gotrueFailure(resp.StatusCode, data)
	}
	var out struct {
		Providers []customProvider `json:"providers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing gotrue provider list: %w", err)
	}
	return out.Providers, nil
}

func (c *Configurator) gotrueCreateProvider(ctx context.Context, base, token string) error {
	want := c.sso.desiredProvider()
	payload := map[string]any{
		"provider_type":     want.ProviderType,
		"identifier":        want.Identifier,
		"name":              want.Name,
		"issuer":            want.Issuer,
		"client_id":         want.ClientID,
		"client_secret":     c.sso.ClientSecret,
		"scopes":            want.Scopes,
		"attribute_mapping": want.AttributeMapping,
	}
	resp, data, err := c.gotrueRequest(ctx, http.MethodPost, base+"/admin/custom-providers", token, payload)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return gotrueFailure(resp.StatusCode, data)
	}
	return nil
}

func (c *Configurator) gotrueDeleteProvider(ctx context.Context, base, token string) error {
	resp, data, err := c.gotrueRequest(ctx, http.MethodDelete, base+"/admin/custom-providers/"+ssoProviderIdentifier, token, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return gotrueFailure(resp.StatusCode, data)
	}
	return nil
}
