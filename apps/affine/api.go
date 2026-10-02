// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
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

// --- Shared workspace membership ---

// workspaceMemberRow is one row of the workspace member list.
//
// AFFiNE answers `workspace.members` with active members and outstanding
// invitations in a single list, told apart by `status`. That is exactly the
// shape a diff needs: an address present under any status is an address that
// needs no new invitation, so a steady-state pass invites nothing.
type workspaceMemberRow struct {
	Email  string `json:"email"`
	Status string `json:"status"`
}

// workspaceMembers reads the member list. `take` is explicit because the
// resolver defaults to 8 rows, and a page that small would hide the users past
// it and re-invite them on every pass.
func (a *affineAPI) workspaceMembers(ctx context.Context, workspaceID string, take int) ([]workspaceMemberRow, error) {
	var out struct {
		Workspace struct {
			Members []workspaceMemberRow `json:"members"`
		} `json:"workspace"`
	}
	err := a.graphql(ctx,
		`query($id: String!, $take: Int) { workspace(id: $id) { members(take: $take) { email status } } }`,
		map[string]any{"id": workspaceID, "take": take},
		&out)
	if err != nil {
		return nil, fmt.Errorf("reading the affine workspace members: %w", err)
	}
	return out.Workspace.Members, nil
}

// inviteResult is one address's outcome from inviteMembers. A refusal is a
// per-row error rather than a failed call, so one bad address in a list of many
// does not lose the invitations that did land.
type inviteResult struct {
	Email    string         `json:"email"`
	InviteID string         `json:"inviteId"`
	Error    map[string]any `json:"error"`
}

// inviteMembers creates a pending membership for each address.
//
// No mail transport is involved. The invitation is a row; the mail is a queued
// job hanging off it, and a server with no SMTP fails the delivery and keeps
// the row. The user finds the invitation in AFFiNE's own notification center,
// whose record is written before the mail is attempted, so the accept action is
// available in-app with no mailer configured.
//
// The InviteID is the invitation row's own id and this response is the only
// place it can be read: `workspace.members` reports the invitee's *user* id in
// that field, not the invitation's.
func (a *affineAPI) inviteMembers(ctx context.Context, workspaceID string, emails []string) ([]inviteResult, error) {
	if len(emails) == 0 {
		return nil, nil
	}
	var out struct {
		InviteMembers []inviteResult `json:"inviteMembers"`
	}
	err := a.graphql(ctx,
		`mutation($id: String!, $emails: [String!]!) { inviteMembers(workspaceId: $id, emails: $emails) { email inviteId error } }`,
		map[string]any{"id": workspaceID, "emails": emails},
		&out)
	if err != nil {
		return nil, fmt.Errorf("inviting members into the affine workspace: %w", err)
	}
	return out.InviteMembers, nil
}

// createWorkspace creates a server workspace owned by the first-user account,
// seeded with a minimal root document so it is "initialized". A workspace
// created without a root doc (the mutation is empty without `init`) spins the
// editor on "Syncing..." and never renders its sidebar, so the AI chat entry
// never appears. A purely client-side workspace (AFFiNE's onboarding) is
// invisible to this GraphQL API, so Bloud creates the workspace itself: that
// is what lets the BYOK profile and MCP credential be registered against it.
func (a *affineAPI) createWorkspace(ctx context.Context, init []byte) (string, error) {
	var out struct {
		CreateWorkspace struct {
			ID string `json:"id"`
		} `json:"createWorkspace"`
	}
	query := `mutation createWorkspace($init: Upload) { createWorkspace(init: $init) { id } }`
	if err := a.graphqlUpload(ctx, query, map[string]any{"init": nil}, "variables.init", "init.bin", init, &out); err != nil {
		return "", fmt.Errorf("creating the affine workspace: %w", err)
	}
	if out.CreateWorkspace.ID == "" {
		return "", fmt.Errorf("creating the affine workspace: no id in the response")
	}
	return out.CreateWorkspace.ID, nil
}

// graphqlUpload runs a GraphQL mutation that carries one `Upload` variable,
// using the graphql-multipart-request-spec: the operations/map fields plus the
// file part. It reuses the same envelope handling as graphql, so a refused
// mutation surfaces its GraphQL error rather than a bare transport status.
func (a *affineAPI) graphqlUpload(ctx context.Context, query string, variables map[string]any, varPath, filename string, file []byte, out any) error {
	csrf, err := a.csrfToken()
	if err != nil {
		return err
	}
	body, contentType, err := multipartGraphQL(query, variables, varPath, filename, file)
	if err != nil {
		return err
	}
	resp, err := a.session.POST("/graphql").
		Anonymous().
		Header("x-csrf-token", csrf).
		Body(body, contentType).
		OK(http.StatusOK).
		NoRetry().
		Do(ctx)
	if err != nil {
		return err
	}
	var env graphqlEnvelope
	if err := json.Unmarshal(resp, &env); err != nil {
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

// multipartGraphQL builds a multipart/form-data body per the
// graphql-multipart-request-spec: `operations` (query + variables, with the
// upload variable nulled), `map` (which part fills which variable path), and
// the file part.
func multipartGraphQL(query string, variables map[string]any, varPath, filename string, file []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	ops, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return nil, "", err
	}
	m, err := json.Marshal(map[string]any{"0": []string{varPath}})
	if err != nil {
		return nil, "", err
	}
	if err := w.WriteField("operations", string(ops)); err != nil {
		return nil, "", err
	}
	if err := w.WriteField("map", string(m)); err != nil {
		return nil, "", err
	}
	part, err := w.CreateFormFile("0", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(file); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
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

// --- BYOK inference provider ---
//
// AFFiNE's built-in AI (copilot) reaches an OpenAI-compatible endpoint through a
// per-workspace BYOK profile: an endpoint URL, a dialect, a credential, and the
// model declarations the endpoint serves. Bloud registers its resolved
// `inference` binding as one such profile, so "bring your own endpoint" is a
// reconciliation instead of a manual workspace setting. The profile lives in
// AFFiNE's database (config.json only opens the policy; see renderConfigFile),
// which is why it is written through AFFiNE's own API and not into a file.
//
// The wire shape is pinned to AFFiNE 0.27.4 (the image in metadata.yaml): the
// copilot-BYOK GraphQL surface is young and not part of a public contract, so a
// future image may rename a field. Callers treat any GraphQL error as "this
// pass did not wire AI," never as a node failure (see ensureInferenceProvider).
const (
	byokProviderOpenAI           = "openai"
	byokEndpointOpenAICompatible = "openai_compatible"
	byokDialectChatCompletions   = "chat_completions"
)

// byokProfile is the slice of a workspace BYOK profile the configurator reads
// back for reconciliation. AFFiNE never returns the stored credential, so the
// only way to detect credential drift is the caller's own fingerprint of what
// it last wrote (see the state file in configurator.go).
type byokProfile struct {
	ProfileID  string `json:"profileId"`
	Provider   string `json:"provider"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	Revision   int    `json:"revision"`
	Definition struct {
		Endpoint struct {
			Kind    string  `json:"kind"`
			URL     *string `json:"url"`
			Dialect *string `json:"dialect"`
		} `json:"endpoint"`
		Models []struct {
			ModelID string `json:"modelId"`
			Enabled bool   `json:"enabled"`
		} `json:"models"`
	} `json:"definition"`
}

// byokSettings is the workspace's BYOK state: the server policy that gates a
// custom endpoint, and the profiles already registered. The policy is what
// tells the configurator whether config.json has taken effect yet.
type byokSettings struct {
	Policy struct {
		Enabled                  bool   `json:"enabled"`
		CustomEndpointMode       string `json:"customEndpointMode"`
		PrivateEndpointSupported bool   `json:"privateEndpointSupported"`
	} `json:"policy"`
	Profiles []byokProfile `json:"profiles"`
}

// byokSettingsQuery reads the policy and the existing profiles. byokSettings
// takes no range arguments (unlike byokUsage, which is not requested here).
const byokSettingsQuery = `query($id: String!) {
  workspace(id: $id) {
    byokSettings {
      policy { enabled customEndpointMode privateEndpointSupported }
      profiles {
        profileId
        provider
        name
        enabled
        revision
        definition {
          endpoint { kind url dialect }
          models { modelId enabled }
        }
      }
    }
  }
}`

func (a *affineAPI) byokSettings(ctx context.Context, workspaceID string) (*byokSettings, error) {
	var out struct {
		Workspace struct {
			ByokSettings byokSettings `json:"byokSettings"`
		} `json:"workspace"`
	}
	if err := a.graphql(ctx, byokSettingsQuery, map[string]any{"id": workspaceID}, &out); err != nil {
		return nil, err
	}
	return &out.Workspace.ByokSettings, nil
}

// byokDefinition is the endpoint + model declaration AFFiNE requires. A model
// needs at least one capability or the mutation is rejected. AFFiNE's chat
// sends a toolsConfig (workspace search + doc reading), which makes the route
// slot require the `tool_calling` feature; without it the route reports
// `no_compatible_target`. Bloud's gateway is an OpenAI-compatible server, so a
// tool-calling request is forwarded as-is.
func byokDefinition(b configurator.InferenceBinding) map[string]any {
	return map[string]any{
		"endpoint": map[string]any{
			"kind":    byokEndpointOpenAICompatible,
			"url":     b.Endpoint,
			"dialect": byokDialectChatCompletions,
		},
		"models": []any{
			map[string]any{
				"modelId": b.DefaultModel,
				"enabled": true,
				"capabilities": []any{
					map[string]any{
						"input":             []string{"text"},
						"output":            []string{"text"},
						"features":          []string{"tool_calling"},
						"attachmentKinds":   []string{},
						"attachmentSources": []string{},
					},
				},
			},
		},
	}
}

// createByokProfile registers the Bloud endpoint as a new workspace BYOK
// profile and returns its id. `description` must be present explicitly (AFFiNE
// rejects the input otherwise), and the credential is required here.
func (a *affineAPI) createByokProfile(ctx context.Context, workspaceID, credential, name, description string, b configurator.InferenceBinding) (string, error) {
	var out struct {
		CreateWorkspaceByokProfile struct {
			ProfileID string `json:"profileId"`
		} `json:"createWorkspaceByokProfile"`
	}
	err := a.graphql(ctx,
		`mutation($input: CreateWorkspaceByokProfileInput!) { createWorkspaceByokProfile(input: $input) { profileId } }`,
		map[string]any{"input": map[string]any{
			"workspaceId": workspaceID,
			"provider":    byokProviderOpenAI,
			"name":        name,
			"description": description,
			"credential":  credential,
			"definition":  byokDefinition(b),
			"enabled":     true,
		}},
		&out)
	if err != nil {
		return "", fmt.Errorf("creating the affine BYOK profile: %w", err)
	}
	if out.CreateWorkspaceByokProfile.ProfileID == "" {
		return "", fmt.Errorf("creating the affine BYOK profile: no profile id")
	}
	return out.CreateWorkspaceByokProfile.ProfileID, nil
}

// replaceByokProfile updates an existing profile under optimistic concurrency
// (expectedRevision). A credential is only sent when non-empty: AFFiNE treats a
// null credential as "keep the stored one," which is what an endpoint-only
// change wants. The empty-string case cannot happen because AFFiNE rejects an
// empty credential on create, so a profile always has one.
func (a *affineAPI) replaceByokProfile(ctx context.Context, workspaceID, profileID, credential, name, description string, revision int, b configurator.InferenceBinding) error {
	var credentialArg any
	if credential != "" {
		credentialArg = credential
	}
	var out struct {
		ReplaceWorkspaceByokProfile struct {
			ProfileID string `json:"profileId"`
		} `json:"replaceWorkspaceByokProfile"`
	}
	return a.graphql(ctx,
		`mutation($input: ReplaceWorkspaceByokProfileInput!) { replaceWorkspaceByokProfile(input: $input) { profileId } }`,
		map[string]any{"input": map[string]any{
			"workspaceId":      workspaceID,
			"profileId":        profileID,
			"expectedRevision": revision,
			"name":             name,
			"description":      description,
			"credential":       credentialArg,
			"definition":       byokDefinition(b),
			"enabled":          true,
		}},
		&out)
}

// rotateByokCredential changes only the stored credential, keeping the
// definition untouched. Used when the endpoint and models are unchanged but the
// gateway key is not, so the profile's revision does not churn.
func (a *affineAPI) rotateByokCredential(ctx context.Context, workspaceID, profileID, credential string, revision int) error {
	var out struct {
		RotateWorkspaceByokCredential struct {
			ProfileID string `json:"profileId"`
		} `json:"rotateWorkspaceByokCredential"`
	}
	return a.graphql(ctx,
		`mutation($input: RotateWorkspaceByokCredentialInput!) { rotateWorkspaceByokCredential(input: $input) { profileId } }`,
		map[string]any{"input": map[string]any{
			"workspaceId":      workspaceID,
			"profileId":        profileID,
			"expectedRevision": revision,
			"credential":       credential,
		}},
		&out)
}

// deleteByokProfile removes a profile Bloud registered. AFFiNE answers with a
// bool that is false when the profile was already gone, which is a successful
// teardown from the caller's point of view.
func (a *affineAPI) deleteByokProfile(ctx context.Context, workspaceID, profileID string) error {
	var out struct {
		DeleteWorkspaceByokProfile bool `json:"deleteWorkspaceByokProfile"`
	}
	return a.graphql(ctx,
		`mutation($workspaceId: String!, $profileId: ID!) { deleteWorkspaceByokProfile(workspaceId: $workspaceId, profileId: $profileId) }`,
		map[string]any{"workspaceId": workspaceID, "profileId": profileID},
		&out)
}
