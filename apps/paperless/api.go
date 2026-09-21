// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package paperless

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// noFollow lets the form clients read a submission's redirect themselves (that
// 302 is the outcome: to the issuer for the SSO button, to the dashboard for a
// successful signup) instead of chasing it into the page behind it.
var noFollow = false

// paperlessAPI is the typed surface over Paperless-ngx's HTTP API. Every
// method reads as declared intent; transport, retry, and timeouts live in
// appclient.
//
// The form clients exist because Django's CSRF protection is cookie-bound and
// session-sensitive: signing up logs the account in and leaves a session
// cookie, and presenting that session to the provider form or to /api/token/
// makes Django demand a CSRF token it did not issue. Each cookie-bound flow
// therefore gets its own jar, and the API client gets none.
type paperlessAPI struct {
	cl         *appclient.Client // no cookies: readiness, page reads, token login
	forms      *appclient.Client // the sign-in page and its SSO button
	signupForm *appclient.Client // the signup form, which leaves a session behind
}

// newAPI builds the typed clients against a base-URL resolver using the shared
// HTTP factory.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *paperlessAPI {
	newFormClient := func() *appclient.Client {
		// The only error case is a nil cookie-jar option, which this call does
		// not pass.
		jar, _ := cookiejar.New(nil)
		return f.New(appclient.Spec{
			Name:            appName,
			BaseURLFn:       baseURLFn,
			Jar:             jar,
			FollowRedirects: &noFollow,
		})
	}
	return &paperlessAPI{
		cl:         f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		forms:      newFormClient(),
		signupForm: newFormClient(),
	}
}

// waitServer polls the sign-in page until it answers 200. The first boot runs
// database migrations before the webserver listener opens, so the window is
// generous.
func (a *paperlessAPI) waitServer(ctx context.Context) error {
	return a.cl.GET(signInPath).
		Interval(2 * time.Second).
		WithRetry(appclient.WaitPolicy).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// waitProviderAdvertised waits until the sign-in page offers the SSO button.
// The page renders that button as a form whose action is the provider's login
// URL, so seeing the path proves the generated config file was read:
// PAPERLESS_APPS put the provider into INSTALLED_APPS and
// PAPERLESS_SOCIALACCOUNT_PROVIDERS configured it.
func (a *paperlessAPI) waitProviderAdvertised(ctx context.Context) error {
	return a.cl.GET(signInPath).
		Interval(3 * time.Second).
		WithRetry(appclient.WaitPolicy).
		Ready(func(status int, body []byte) bool {
			return status == http.StatusOK && bytes.Contains(body, []byte(providerLoginPath))
		}).
		Wait(ctx)
}

// probeProviderLogin submits the SSO button's form and requires the app to hand
// the browser to the issuer rather than to an error. allauth fetches the
// issuer's discovery document to build that redirect, so the 302 proves the
// issuer is reachable from inside the container and the registered client
// resolves. What it cannot see is the redirect URI itself, since that is a
// response header and appclient reports status and body only: the Go
// integration test and the browser journey assert it instead.
func (a *paperlessAPI) probeProviderLogin(ctx context.Context) error {
	page, err := a.forms.GET(signInPath).Anonymous().OK(http.StatusOK).NoRetry().Do(ctx)
	if err != nil {
		return err
	}
	match := csrfTokenRe.FindSubmatch(page)
	if match == nil {
		return fmt.Errorf("no CSRF token in the sign-in form")
	}
	return a.forms.POST(providerLoginPath).
		Anonymous().
		Form(url.Values{"csrfmiddlewaretoken": {string(match[1])}}).
		OK(http.StatusFound, http.StatusSeeOther).
		NoRetry().
		Exec(ctx)
}

// login exchanges credentials for an API token, which doubles as the check
// that the internal admin account exists with the configured password. An
// empty token with a nil error means the endpoint accepted neither as a
// credential: it rejected them (400) or throttled the request (429), which the
// caller reports as "unverified" rather than as a node failure.
func (a *paperlessAPI) login(ctx context.Context, username, password string) (string, error) {
	body, err := a.cl.POST("/api/token/").
		Anonymous().
		JSON(map[string]string{"username": username, "password": password}).
		OK(http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests).
		Do(ctx)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	return out.Token, nil
}

// signupOpen reports whether Paperless-ngx still offers its signup form, which
// it does only while no user exists (CustomAccountAdapter.is_open_for_signup).
// The page renders the password fields when signup is open and "Sign Up
// Closed" when it is not, so the probe looks for the form itself.
func (a *paperlessAPI) signupOpen(ctx context.Context) (bool, error) {
	body, err := a.cl.GET(signupPath).
		Anonymous().
		OK(http.StatusOK).
		NoRetry().
		Do(ctx)
	if err != nil {
		return false, err
	}
	return bytes.Contains(body, []byte(`name="password1"`)), nil
}

// signup creates the first account. Paperless-ngx has no admin API and no
// superuser before the first signup: its own first-run flow is this form, and
// the account adapter promotes whatever account it creates to superuser. The
// form is CSRF-protected, so the token is read from the page and posted back
// with the cookie it was issued against; the no-follow client is used so the
// success redirect is observed rather than chased into the dashboard (a
// rejected form re-renders the page with 200 instead).
func (a *paperlessAPI) signup(ctx context.Context, username, email, password string) error {
	form, err := a.signupForm.GET(signupPath).Anonymous().OK(http.StatusOK).NoRetry().Do(ctx)
	if err != nil {
		return err
	}
	match := csrfTokenRe.FindSubmatch(form)
	if match == nil {
		return fmt.Errorf("no CSRF token in the signup form")
	}

	return a.signupForm.POST(signupPath).
		Anonymous().
		Form(url.Values{
			"csrfmiddlewaretoken": {string(match[1])},
			"username":            {username},
			"email":               {email},
			"password1":           {password},
			"password2":           {password},
		}).
		OK(http.StatusFound, http.StatusSeeOther).
		NoRetry().
		Exec(ctx)
}

// csrfTokenRe finds the hidden CSRF input Django renders in a form.
var csrfTokenRe = regexp.MustCompile(`name="csrfmiddlewaretoken"\s+value="([^"]+)"`)
