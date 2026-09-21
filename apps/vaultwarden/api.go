// SPDX-License-Identifier: AGPL-3.0-only

package vaultwarden

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

const (
	// alivePath answers 200 once the server is serving. It is the endpoint the
	// image's own healthcheck uses.
	alivePath = "/alive"

	// prevalidatePath is the first call the Bitwarden clients make when a user
	// starts an SSO login. It answers 200 with a token when SSO is enabled and
	// 400 ("SSO sign-in is not available") when it is not, which makes it the
	// probe that the generated env file was actually read by the running app.
	prevalidatePath = "/identity/sso/prevalidate"

	// authorizePath starts the OpenID Connect authorization-code flow. The app
	// fetches the issuer's discovery document to build the redirect, so a 3xx
	// answer proves the issuer is reachable from inside the container and the
	// configured authority resolves; a 400 is what an unreachable or
	// mismatched issuer produces.
	authorizePath = "/identity/connect/authorize"

	// webClientID is the OAuth client id the Bitwarden web vault presents; the
	// authorize endpoint only serves the first-party client ids.
	webClientID = "web"

	// probeVerifier is a fixed PKCE verifier for the probe. Nothing is ever
	// exchanged for it, so it needs no secrecy, only a valid S256 challenge.
	probeVerifier = "bloud-vaultwarden-sso-probe-verifier-0123456789"
)

// noFollow lets the probe read the authorize endpoint's redirect itself (that
// 3xx is the outcome) instead of chasing it to the identity provider.
var noFollow = false

// vaultwardenAPI is the typed surface over Vaultwarden's HTTP API. Every method
// reads as declared intent; transport, retry, and timeouts live in appclient.
type vaultwardenAPI struct {
	cl         *appclient.Client // readiness and JSON reads
	noRedirect *appclient.Client // the authorize probe, which must see the redirect
}

// newAPI builds the typed clients against a base-URL resolver using the shared
// HTTP factory.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *vaultwardenAPI {
	return &vaultwardenAPI{
		cl: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		noRedirect: f.New(appclient.Spec{
			Name:            appName,
			BaseURLFn:       baseURLFn,
			FollowRedirects: &noFollow,
		}),
	}
}

// waitAlive polls /alive until the server answers 200. The first boot creates
// the database and RSA keys before it listens, so the window is generous.
func (a *vaultwardenAPI) waitAlive(ctx context.Context) error {
	return a.cl.GET(alivePath).
		Interval(2 * time.Second).
		WithRetry(appclient.WaitPolicy).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// ssoPrevalidate asks the app whether SSO sign-in is available and returns the
// token it issues for the authorize call. A 400 means SSO is off in the running
// app, i.e. the generated config was not applied.
func (a *vaultwardenAPI) ssoPrevalidate(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"token"`
	}
	if err := a.cl.GET(prevalidatePath).OK(http.StatusOK).NoRetry().DoInto(ctx, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", fmt.Errorf("SSO prevalidate answered without a token")
	}
	return out.Token, nil
}

// probeAuthorize starts an authorization request the way the web vault does and
// requires the app to hand the browser to the issuer rather than to an error.
// The redirect URI itself is a response header, which appclient does not
// report, so the Go integration test and the browser journey assert it.
func (a *vaultwardenAPI) probeAuthorize(ctx context.Context, publicURL, ssoToken string) error {
	challenge := sha256.Sum256([]byte(probeVerifier))
	return a.noRedirect.GET(authorizePath).
		Query("client_id", webClientID).
		Query("redirect_uri", publicURL+"/sso-connector.html").
		Query("response_type", "code").
		Query("scope", "api offline_access").
		Query("state", "bloud-probe").
		Query("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])).
		Query("code_challenge_method", "S256").
		Query("response_mode", "query").
		Query("domain_hint", "bloud").
		Query("ssoToken", ssoToken).
		OK(http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect).
		NoRetry().
		Exec(ctx)
}
