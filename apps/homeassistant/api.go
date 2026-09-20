// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package homeassistant

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// haAPI is the typed surface over Home Assistant's HTTP API. Every method reads
// as declared intent; transport, retry, timeouts, and redirect policy live in
// appclient behind the single client. HA's owner token is one-shot (created
// during onboarding, never refetchable), so it is passed per-call as a bearer
// header rather than through a managed TokenSpec.
type haAPI struct {
	cl *appclient.Client
}

// noFollow redirects so the OIDC-trust and liveness probes can inspect the 3xx
// themselves (the trust probe reads the forward middleware's answer, which a
// followed redirect would hide).
var noFollow = false

// newAPI builds the typed client against a base-URL resolver using the shared
// HTTP factory. Redirects are never followed (see noFollow).
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *haAPI {
	return &haAPI{cl: f.New(appclient.Spec{
		Name:            appName,
		BaseURLFn:       baseURLFn,
		FollowRedirects: &noFollow,
	})}
}

// fixedWaitPolicy is a constant-cadence policy for readiness waits: it sleeps
// the same interval between every attempt (MaxInterval pins the factor-clamped
// backoff) and leaves the overall bound to the caller's context deadline. This
// reproduces Home Assistant's "poll every pollInterval until the outer
// PostStart deadline" behavior exactly, with one implementation.
func fixedWaitPolicy(iv time.Duration) appclient.RetryPolicy {
	if iv <= 0 {
		iv = 2 * time.Second
	}
	return appclient.RetryPolicy{MaxAttempts: 0, Initial: iv, MaxInterval: iv}
}

// --- readiness waits (composers over a single poll each) ---

// waitAPI polls /api/ (public, answers "API running" without auth) until the
// HTTP listener answers anything under 500. Connection errors and 5xx mean HA
// is still booting and are retried.
func (a *haAPI) waitAPI(ctx context.Context, iv time.Duration) error {
	return a.cl.GET("/api/").
		AlreadyDoneFunc(func(status int, _ []byte) bool { return status < 500 }).
		Ready(appclient.StatusLT(500)).
		WithRetry(fixedWaitPolicy(iv)).
		Wait(ctx)
}

// waitProxyTrust polls the forwarded-header probe until the running process
// accepts forwarded requests. A 400 (stale process that has not reloaded) is
// NOT ready and keeps being polled; a 5xx / refused connection (still booting /
// mid-restart) is retried; any other answer (401 Bearer once trust is loaded,
// or a 2xx/3xx) means the forward middleware let the request through.
// Returning nil guarantees the node never goes RUNNING on a proxy-rejecting
// process.
func (a *haAPI) waitProxyTrust(ctx context.Context, iv time.Duration) error {
	return a.cl.GET("/api/").
		Header("X-Forwarded-For", xffProbeAddr).
		AlreadyDoneFunc(func(status int, _ []byte) bool { return status != http.StatusBadRequest && status < 500 }).
		Ready(appclient.StatusNot(http.StatusBadRequest)).
		WithRetry(fixedWaitPolicy(iv)).
		Wait(ctx)
}

// waitOIDCReady verifies the OIDC auth provider is live by probing
// /auth/oidc/welcome. hass-oidc-auth registers that view only when its
// async_setup succeeds and provider discovery passes, so a 200 proves both. The
// route 404s while HA is still booting, so it is retried until the deadline.
func (a *haAPI) waitOIDCReady(ctx context.Context, iv time.Duration) error {
	return a.cl.GET("/auth/oidc/welcome").
		Ready(appclient.StatusIs(http.StatusOK)).
		WithRetry(fixedWaitPolicy(iv)).
		Wait(ctx)
}

// probeProxyTrust is a single-shot version of the trust read used by PreStart's
// stale-process self-heal (it must probe once, not block). It reports the
// three states the wait cannot express:
//
//	trusted=true                 → a 2xx/3xx/4xx other than 400: the forward
//	                              middleware passed the forwarded request.
//	trusted=false, reachable=true→ 400 (stale, forward-rejecting) or 5xx
//	                              (still booting): the process answered but
//	                              does not yet honour forwarded headers.
//	reachable=false              → never connected (down / mid-restart).
func (a *haAPI) probeProxyTrust(ctx context.Context) (trusted, reachable bool, status int, perr error) {
	_, err := a.cl.GET("/api/").
		Header("X-Forwarded-For", xffProbeAddr).
		NoRetry().
		Timeout(10 * time.Second).
		Do(ctx)
	if err == nil {
		return true, true, http.StatusOK, nil
	}
	// Status 0 is a transport-level failure (connection refused/reset/DNS):
	// the process never answered, so it is not reachable.
	st := appclient.StatusOf(err)
	if st == 0 {
		return false, false, 0, err
	}
	if st == http.StatusBadRequest || st >= 500 {
		return false, true, st, nil
	}
	return true, true, st, nil
}

// onboardingStatus retries GET /api/onboarding until one of:
//   - a 200 answer (the step list is returned),
//   - a permanent 404 alongside an owner in the auth store (Home Assistant
//     deregisters the endpoint once every step is closed), reported as
//     alreadyOnboarded=true,
//   - the caller's context deadline.
//
// During boot the HTTP listener is already up (waitAPI passes on any <500)
// while this route still 404s, so a bare 404 (owner not yet on disk) keeps
// being retried. This needs the response body and the two distinct 404
// meanings at once (a body the Wait primitive does not surface), so it is a
// thin loop over the framework's Do rather than a Wait.
func (a *haAPI) onboardingStatus(ctx context.Context, iv time.Duration, ownerPresent func() bool) ([]byte, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, false, fmt.Errorf("timed out waiting for the Home Assistant onboarding API (HTTP listener is up but /api/onboarding keeps failing): %w", err)
		}
		body, err := a.cl.GET("/api/onboarding").NoRetry().Do(ctx)
		if err == nil {
			return body, false, nil
		}
		if appclient.StatusOf(err) == http.StatusNotFound && ownerPresent != nil && ownerPresent() {
			return nil, true, nil
		}
		if iv <= 0 {
			iv = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return nil, false, fmt.Errorf("timed out waiting for the Home Assistant onboarding API (HTTP listener is up but /api/onboarding keeps failing): %w", ctx.Err())
		case <-time.After(iv):
		}
	}
}

// --- onboarding verbs (declared outcomes; idempotent replays are declared) ---

// createOwner POSTs the first-run owner and returns the one-shot authorization
// code. HA hands back an auth_code, never an access token, from this endpoint.
func (a *haAPI) createOwner(ctx context.Context, payload map[string]string) (string, error) {
	var resp struct {
		AuthCode string `json:"auth_code"`
	}
	if err := a.cl.POST("/api/onboarding/users").JSON(payload).
		OK(http.StatusOK, http.StatusCreated).
		DoInto(ctx, &resp); err != nil {
		return "", err
	}
	if resp.AuthCode == "" {
		return "", fmt.Errorf("onboarding response carried no authorization code")
	}
	return resp.AuthCode, nil
}

// exchangeAuthCode trades a one-shot authorization code for the owner access
// token via POST /auth/token (authorization_code grant, built-in iOS client id,
// no redirect_uri or client secret).
func (a *haAPI) exchangeAuthCode(ctx context.Context, authCode string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"},
		"client_id":  {onboardingClientID},
		"code":       {authCode},
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := a.cl.POST("/auth/token").Form(form).
		OK(http.StatusOK).
		DoInto(ctx, &tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("token exchange returned no access token")
	}
	return tok.AccessToken, nil
}

// finishStep closes one onboarding step that follows "user" with the owner
// bearer token. A 403 means a previous reconciliation already closed the step:
// declared AlreadyDone so the idempotent replay is a no-op, not a string
// match on the body.
func (a *haAPI) finishStep(ctx context.Context, path, body, token string) error {
	return a.cl.POST(path).
		Body([]byte(body), "application/json").
		Header("Authorization", "Bearer "+token).
		OK(http.StatusOK, http.StatusCreated).
		AlreadyDone(http.StatusForbidden).
		Exec(ctx)
}
