// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// affineAPI is the typed surface over AFFiNE's HTTP API.
//
// Two clients, because AFFiNE has two auth positions. The anonymous one reads
// the public endpoints the configurator uses to check liveness and SSO wiring.
// The session one signs in as the bootstrap owner and carries the session
// cookie plus the CSRF token, which is what every GraphQL call needs: AFFiNE
// derives the acting user from the cookie, so there is no header-only
// credential for a server-side caller.
type affineAPI struct {
	cl      *appclient.Client
	session *appclient.Client
	jar     http.CookieJar
	baseURL func() string
}

// newAPI builds the typed client against a base-URL resolver.
func newAPI(f configurator.ClientFactory, baseURLFn func() string) *affineAPI {
	// The only error case is a nil cookie-jar option, which this call does
	// not pass.
	jar, _ := cookiejar.New(nil)
	return &affineAPI{
		cl:      f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		session: f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn, Jar: jar}),
		jar:     jar,
		baseURL: baseURLFn,
	}
}

// waitServer polls /info (public) until the server answers 200. The first
// boot runs prisma migrations before the HTTP listener opens.
func (a *affineAPI) waitServer(ctx context.Context) error {
	return a.cl.GET("/info").
		Interval(2 * time.Second).
		Within(5 * time.Minute).
		Ready(appclient.StatusIs(http.StatusOK)).
		Wait(ctx)
}

// ensureOwner creates the first-run owner account. AFFiNE only accepts the
// call before any user exists and answers 403 "First user already created"
// otherwise: the idempotency signal for later reconciliation passes.
func (a *affineAPI) ensureOwner(ctx context.Context, name, email, password string) (bool, error) {
	return a.cl.POST("/api/setup/create-admin-user").
		JSON(map[string]string{"name": name, "email": email, "password": password}).
		OK(http.StatusOK, http.StatusCreated).
		// AFFiNE has no dedicated code for this case; documented in INTEGRATION.md.
		AlreadyDoneFunc(func(s int, b []byte) bool {
			return s == http.StatusForbidden && bytes.Contains(b, []byte("First user already created"))
		}).
		Ensure(ctx)
}

// waitForOIDCPreflight polls the public OAuth preflight endpoint until it
// returns the authorization URL. The server validates the issuer
// asynchronously after boot (with backoff), so allow a generous window.
func (a *affineAPI) waitForOIDCPreflight(ctx context.Context) error {
	return a.cl.POST("/api/oauth/preflight").
		Anonymous().
		JSON(map[string]string{"provider": "OIDC", "client": "web", "client_nonce": "bloud-poststart-check"}).
		Interval(3 * time.Second).
		Within(3 * time.Minute).
		Ready(func(s int, b []byte) bool {
			return s == http.StatusOK && strings.Contains(string(b), "\"url\"")
		}).
		Wait(ctx)
}

// --- MCP credential minting ---
//
// AFFiNE ships its own MCP server (verified on 0.27.4): a stateless
// streamable-HTTP endpoint at /api/workspaces/<id>/mcp that authenticates a
// `aff_mcp_v1.<credentialId>.<secret>` bearer. Bloud does not proxy it and
// does not invent a credential for it. It signs in as the owner it
// bootstrapped, asks AFFiNE to mint a scoped credential through AFFiNE's own
// API, and publishes what comes back. That is the "real keys only" rule: the
// published string is one AFFiNE created and validates, so revoking it in the
// AFFiNE UI revokes MCP access and nothing else.

// mcpPath builds the MCP endpoint path for one workspace.
func mcpPath(workspaceID string) string {
	return "/api/workspaces/" + workspaceID + "/mcp"
}

// signIn establishes the session the GraphQL calls run as. AFFiNE self-host
// accepts a password sign-in (unlike AFFiNE Cloud, where Cloudflare blocks
// programmatic sign-in), and the response sets both the session cookie and the
// CSRF token the next write must echo back.
func (a *affineAPI) signIn(ctx context.Context, email, password string) error {
	_, err := a.session.POST("/api/auth/sign-in").
		Anonymous().
		JSON(map[string]string{"email": email, "password": password}).
		OK(http.StatusOK, http.StatusCreated).
		NoRetry().
		Do(ctx)
	if err != nil {
		return fmt.Errorf("signing in as the affine owner: %w", err)
	}
	return nil
}

// csrfToken reads the CSRF token the session was issued. AFFiNE hands it as a
// cookie rather than in a response body, so it comes out of the jar the
// session client writes to.
func (a *affineAPI) csrfToken() (string, error) {
	u, err := url.Parse(a.baseURL())
	if err != nil {
		return "", fmt.Errorf("parsing affine base url: %w", err)
	}
	for _, c := range a.jar.Cookies(u) {
		if c.Name == csrfCookieName && c.Value != "" {
			return c.Value, nil
		}
	}
	return "", fmt.Errorf("no %s cookie after sign-in", csrfCookieName)
}

// graphqlErr is one GraphQL-level error. AFFiNE reports a refused mutation as a
// 400/403 with an `errors` array, so a non-2xx status alone never tells the
// configurator what was rejected.
type graphqlErr struct {
	Message string `json:"message"`
}

type graphqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlErr    `json:"errors"`
}

// graphql runs one request as the signed-in owner and unmarshals `data` into
// out. A GraphQL error is returned as an error even when the transport succeeded,
// because the envelope carries the rejection reason and swallowing it would make
// a refused mutation look like an empty result.
func (a *affineAPI) graphql(ctx context.Context, query string, variables map[string]any, out any) error {
	csrf, err := a.csrfToken()
	if err != nil {
		return err
	}
	body, err := a.session.POST("/graphql").
		Anonymous().
		Header("x-csrf-token", csrf).
		JSON(map[string]any{"query": query, "variables": variables}).
		OK(http.StatusOK).
		NoRetry().
		Do(ctx)
	if err != nil {
		return err
	}
	var env graphqlEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("parsing graphql response: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("graphql: %s", env.Errors[0].Message)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("parsing graphql data: %w", err)
	}
	return nil
}

// listWorkspaces returns the owner's workspace ids, in the order AFFiNE
// reports them.
func (a *affineAPI) listWorkspaces(ctx context.Context) ([]string, error) {
	var out struct {
		Workspaces []struct {
			ID string `json:"id"`
		} `json:"workspaces"`
	}
	if err := a.graphql(ctx, `query { workspaces { id } }`, nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Workspaces))
	for _, w := range out.Workspaces {
		ids = append(ids, w.ID)
	}
	return ids, nil
}

// createWorkspace creates a workspace owned by the bootstrap account. The
// `init` upload the mutation also accepts is the local-first document a browser
// seeds a new workspace with; it is optional, and omitting it is what makes a
// server-side create possible: the workspace is created empty and the owner
// fills it in.
func (a *affineAPI) createWorkspace(ctx context.Context) (string, error) {
	var out struct {
		CreateWorkspace struct {
			ID string `json:"id"`
		} `json:"createWorkspace"`
	}
	if err := a.graphql(ctx, `mutation { createWorkspace { id } }`, nil, &out); err != nil {
		return "", fmt.Errorf("creating the bloud workspace: %w", err)
	}
	if out.CreateWorkspace.ID == "" {
		return "", fmt.Errorf("creating the bloud workspace: no id in the response")
	}
	return out.CreateWorkspace.ID, nil
}

// createMcpCredential mints a workspace-scoped MCP credential and returns the
// bearer token. The token is revealed only by the call that creates it, which
// is why the caller stores it rather than re-reading the credential list.
//
// READ_ONLY is deliberate and not a caution: on a stable release AFFiNE rejects
// a READ_WRITE credential outright ("MCP write tools are not available")
// unless the server runs in dev or canary, so asking for write would fail every
// pass on the pinned image.
func (a *affineAPI) createMcpCredential(ctx context.Context, workspaceID, name string, expirationDays int) (string, error) {
	var out struct {
		CreateMcpCredential struct {
			Token string `json:"token"`
		} `json:"createMcpCredential"`
	}
	err := a.graphql(ctx,
		`mutation($input: CreateMcpCredentialInput!) { createMcpCredential(input: $input) { token } }`,
		map[string]any{"input": map[string]any{
			"name":           name,
			"workspaceId":    workspaceID,
			"accessMode":     "READ_ONLY",
			"expirationDays": expirationDays,
		}},
		&out)
	if err != nil {
		return "", fmt.Errorf("minting the affine MCP credential: %w", err)
	}
	if out.CreateMcpCredential.Token == "" {
		return "", fmt.Errorf("minting the affine MCP credential: empty token")
	}
	return out.CreateMcpCredential.Token, nil
}

// probeMCP calls tools/list against the workspace's MCP endpoint with the given
// bearer. It distinguishes a credential that works from one that does not:
// a live 200 means the published token is valid, and a 401/403 means it is
// dead (revoked or expired) and a replacement should be minted. Any other
// outcome is a transport or server fault, reported as retryLater so the caller
// mints nothing rather than piling up spare credentials behind a blip.
func (a *affineAPI) probeMCP(ctx context.Context, path, token string) (works bool, authRejected bool, err error) {
	body, reqErr := a.cl.POST(path).
		Anonymous().
		Header("Authorization", "Bearer "+token).
		Header("Accept", "application/json, text/event-stream").
		JSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/list",
		}).
		OK(http.StatusOK, http.StatusUnauthorized, http.StatusForbidden).
		NoRetry().
		Do(ctx)
	if reqErr != nil {
		return false, false, reqErr
	}
	switch {
	case bytes.Contains(body, []byte(`"tools"`)):
		return true, false, nil
	default:
		return false, true, nil
	}
}
