// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// storeSecrets is an in-memory AppSecretsProvider that actually keeps what the
// configurator publishes, so the MCP flow's read-modify-publish cycle (probe
// the stored token, adopt the stored workspace) can be exercised across passes.
type storeSecrets struct {
	mu       sync.Mutex
	password string
	secrets  map[string]string
	values   map[string]string
}

func newStoreSecrets(password string) *storeSecrets {
	return &storeSecrets{
		password: password,
		secrets:  map[string]string{},
		values:   map[string]string{},
	}
}

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) { return s.password, nil }

func (s *storeSecrets) GetAppSecret(_, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[key]
}

func (s *storeSecrets) SetAppSecret(_, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[key] = value
	return nil
}

func (s *storeSecrets) SetAppContractValue(_, contract, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[contract+"/"+key] = value
	return nil
}

func (s *storeSecrets) GetAppContractValue(_, contract, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.values[contract+"/"+key]
}

// fakeAffine is a stand-in for the slice of AFFiNE the MCP provider touches:
// the owner sign-in that issues the session and CSRF cookies, the GraphQL
// surface for workspace listing and credential minting, and the workspace MCP
// endpoint that validates the bearer.
//
// It records calls so a test can assert what the configurator did rather than
// only what it ended up storing: minting a credential on every reconciliation
// pass would be a leak the stored value alone cannot show.
type fakeAffine struct {
	mu sync.Mutex

	workspaces []string
	// minted is the number of createMcpCredential calls that succeeded.
	minted int
	// validTokens are the bearers the MCP endpoint accepts.
	validTokens map[string]bool
	// mintErr, when set, makes the mint mutation fail the way AFFiNE's
	// READ_WRITE gate does: a 400 with an errors envelope.
	mintErr string
	// requireCSRF mirrors AFFiNE refusing a GraphQL write with no token.
	requireCSRF bool
	// createInit records the Yjs root document Bloud uploaded with the
	// createWorkspace mutation, so a test can assert the workspace is seeded.
	createInit []byte

	// --- BYOK (inference) state ---

	// byokProfiles is the stored profile list, in the shape the settings query
	// returns it.
	byokProfiles []map[string]any
	// byokPolicyMode is the customEndpointMode the settings query reports.
	// Empty means "enabled"; set it to "disabled" to model a server whose
	// config.json did not take effect.
	byokPolicyMode string
	// byokCreates/byokReplaces/byokRotates/byokDeletes count the mutations so a
	// test can prove a steady-state pass touches nothing.
	byokCreates  int
	byokReplaces int
	byokRotates  int
	byokDeletes  int
	// byokLast* record the values of the most recent write, so a test can
	// assert what Bloud actually sent rather than only what it stored.
	byokLastEndpoint   string
	byokLastModel      string
	byokLastCredential string
}

func newFakeAffine(workspaces ...string) *fakeAffine {
	return &fakeAffine{workspaces: workspaces, validTokens: map[string]bool{}}
}

func (f *fakeAffine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/auth/sign-in":
		http.SetCookie(w, &http.Cookie{Name: "affine_session", Value: "sess-1", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "csrf-1", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","email":"bloud-admin@affine.localhost"}`))

	case r.URL.Path == "/graphql":
		f.graphql(w, r)

	case strings.HasPrefix(r.URL.Path, "/api/workspaces/") && strings.HasSuffix(r.URL.Path, "/mcp"):
		f.mcp(w, r)

	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAffine) graphql(w http.ResponseWriter, r *http.Request) {
	if f.requireCSRF && r.Header.Get("x-csrf-token") == "" {
		writeGraphQLErr(w, "missing csrf token")
		return
	}
	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		// graphql-multipart-request-spec: the query lives in the `operations`
		// form field, the file is part "0".
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeGraphQLErr(w, "bad multipart request")
			return
		}
		var op struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(r.FormValue("operations")), &op); err != nil {
			writeGraphQLErr(w, "bad operations field")
			return
		}
		req.Query = op.Query
	} else {
		body := newDecoder(r)
		if err := body.Decode(&req); err != nil {
			writeGraphQLErr(w, "bad request")
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	// The BYOK surface is handled separately so this switch stays under the
	// cyclop gate as the fake grows with the app's API.
	if f.graphqlByok(w, req.Query, req.Variables) {
		return
	}

	switch {
	case strings.Contains(req.Query, "workspaces { id }"):
		ids := make([]map[string]string, 0, len(f.workspaces))
		for _, id := range f.workspaces {
			ids = append(ids, map[string]string{"id": id})
		}
		writeGraphQLData(w, map[string]any{"workspaces": ids})

	case strings.Contains(req.Query, "createWorkspace("):
		id := fmt.Sprintf("ws-%d", len(f.workspaces)+1)
		f.workspaces = append(f.workspaces, id)
		if file, _, err := r.FormFile("0"); err == nil {
			if data, err := io.ReadAll(file); err == nil {
				f.createInit = data
			}
			_ = file.Close()
		}
		writeGraphQLData(w, map[string]any{"createWorkspace": map[string]string{"id": id}})

	case strings.Contains(req.Query, "createMcpCredential"):
		if f.mintErr != "" {
			writeGraphQLErr(w, f.mintErr)
			return
		}
		input, _ := req.Variables["input"].(map[string]any)
		if got, _ := input["accessMode"].(string); got != "READ_ONLY" {
			writeGraphQLErr(w, "unexpected access mode: "+got)
			return
		}
		token := fmt.Sprintf("aff_mcp_v1.cred-%d.secret", f.minted+1)
		f.minted++
		f.validTokens[token] = true
		writeGraphQLData(w, map[string]any{
			"createMcpCredential": map[string]string{"token": token},
		})

	default:
		writeGraphQLErr(w, "unhandled query: "+req.Query)
	}
}

// graphqlByok handles the copilot-BYOK queries and mutations. It returns true
// when it wrote a response, leaving the caller's switch to the MCP surface.
// Called with f.mu held.
func (f *fakeAffine) graphqlByok(w http.ResponseWriter, query string, variables map[string]any) bool {
	switch {
	case strings.Contains(query, "byokSettings"):
		mode := f.byokPolicyMode
		if mode == "" {
			mode = "enabled"
		}
		workspaceID, _ := variables["id"].(string)
		var profiles []map[string]any
		for _, p := range f.byokProfiles {
			if p["workspaceId"] == workspaceID {
				profiles = append(profiles, p)
			}
		}
		if profiles == nil {
			profiles = []map[string]any{}
		}
		writeGraphQLData(w, map[string]any{"workspace": map[string]any{
			"byokSettings": map[string]any{
				"policy": map[string]any{
					"enabled":                  true,
					"customEndpointMode":       mode,
					"privateEndpointSupported": true,
				},
				"profiles": profiles,
			},
		}})

	case strings.Contains(query, "createWorkspaceByokProfile"):
		input, _ := variables["input"].(map[string]any)
		f.byokCreates++
		f.byokLastEndpoint = endpointURL(input)
		f.byokLastModel = firstModelID(input)
		f.byokLastCredential, _ = input["credential"].(string)
		id := fmt.Sprintf("byok-%d", f.byokCreates)
		f.byokProfiles = append(f.byokProfiles, profileFromInput(id, input))
		writeGraphQLData(w, map[string]any{
			"createWorkspaceByokProfile": map[string]string{"profileId": id},
		})

	case strings.Contains(query, "replaceWorkspaceByokProfile"):
		input, _ := variables["input"].(map[string]any)
		f.byokReplaces++
		f.byokLastEndpoint = endpointURL(input)
		f.byokLastModel = firstModelID(input)
		// The replace input is nullable-credential: absent means "keep the
		// stored one", which the fake models by leaving it blank.
		f.byokLastCredential, _ = input["credential"].(string)
		pid, _ := input["profileId"].(string)
		for i := range f.byokProfiles {
			if f.byokProfiles[i]["profileId"] == pid {
				f.byokProfiles[i] = profileFromInput(pid, input)
			}
		}
		writeGraphQLData(w, map[string]any{
			"replaceWorkspaceByokProfile": map[string]string{"profileId": pid},
		})

	case strings.Contains(query, "rotateWorkspaceByokCredential"):
		input, _ := variables["input"].(map[string]any)
		f.byokRotates++
		f.byokLastCredential, _ = input["credential"].(string)
		pid, _ := input["profileId"].(string)
		writeGraphQLData(w, map[string]any{
			"rotateWorkspaceByokCredential": map[string]string{"profileId": pid},
		})

	case strings.Contains(query, "deleteWorkspaceByokProfile"):
		f.byokDeletes++
		pid, _ := variables["profileId"].(string)
		kept := make([]map[string]any, 0, len(f.byokProfiles))
		for _, p := range f.byokProfiles {
			if p["profileId"] != pid {
				kept = append(kept, p)
			}
		}
		f.byokProfiles = kept
		writeGraphQLData(w, map[string]any{"deleteWorkspaceByokProfile": true})

	default:
		return false
	}
	return true
}

func (f *fakeAffine) mcp(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	token := strings.TrimPrefix(auth, "Bearer ")
	f.mu.Lock()
	ok := f.validTokens[token]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"authentication failed"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"doc_search"},{"name":"read_document"}]}}`))
}

func (f *fakeAffine) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.minted
}

func (f *fakeAffine) createInitBytes() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createInit
}

// --- BYOK accessors (locked: the httptest server runs handlers on its own
// goroutines, so a test must not read the fields directly) ---

func (f *fakeAffine) byokStats() (creates, replaces, rotates, deletes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byokCreates, f.byokReplaces, f.byokRotates, f.byokDeletes
}

func (f *fakeAffine) byokLastWrite() (endpoint, model, credential string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.byokLastEndpoint, f.byokLastModel, f.byokLastCredential
}

func (f *fakeAffine) byokProfileCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byokProfiles)
}

func (f *fakeAffine) byokProfileID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.byokProfiles) == 0 {
		return ""
	}
	id, _ := f.byokProfiles[0]["profileId"].(string)
	return id
}

// byokDeleteExternally models an operator removing the profile in the AFFiNE
// UI: the next pass must notice the absence and recreate it.
func (f *fakeAffine) byokDeleteExternally() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byokProfiles = nil
}

func (f *fakeAffine) setByokPolicyMode(mode string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byokPolicyMode = mode
}

// endpointURL and firstModelID read the endpoint and first model out of a BYOK
// mutation input, following the nested definition the GraphQL input carries.
func endpointURL(input map[string]any) string {
	def, _ := input["definition"].(map[string]any)
	ep, _ := def["endpoint"].(map[string]any)
	u, _ := ep["url"].(string)
	return u
}

func firstModelID(input map[string]any) string {
	def, _ := input["definition"].(map[string]any)
	models, _ := def["models"].([]any)
	if len(models) == 0 {
		return ""
	}
	m, _ := models[0].(map[string]any)
	id, _ := m["modelId"].(string)
	return id
}

func profileFromInput(id string, input map[string]any) map[string]any {
	name, _ := input["name"].(string)
	provider, _ := input["provider"].(string)
	enabled, _ := input["enabled"].(bool)
	return map[string]any{
		"profileId":   id,
		"workspaceId": input["workspaceId"],
		"provider":    provider,
		"name":        name,
		"enabled":     enabled,
		"revision":    1,
		"definition":  input["definition"],
	}
}

func newDecoder(r *http.Request) *json.Decoder { return json.NewDecoder(r.Body) }

func writeGraphQLData(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func writeGraphQLErr(w http.ResponseWriter, msg string) {
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]string{{"message": msg}}})
}

// mcpConfigurator wires a configurator to a fake AFFiNE and a recording store.
func mcpConfigurator(t *testing.T, fake *fakeAffine, secrets *storeSecrets) *Configurator {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = server.URL
	return c
}

// The first pass on an instance with no workspaces creates one, mints a
// READ_ONLY credential, and publishes both the bearer and the workspace-scoped
// path. This is the whole provider contract in one pass.
func TestEnsureMCPCredential_FirstPassCreatesAndPublishes(t *testing.T) {
	fake := newFakeAffine()
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())

	require.Len(t, fake.workspaces, 1, "an instance with no workspaces gets one created")
	assert.Equal(t, 1, fake.mintCount())
	assert.Equal(t, workspaceInitDoc, fake.createInitBytes(), "the workspace is seeded with a root document so the editor initializes")

	workspaceID := fake.workspaces[0]
	assert.Equal(t, mcpPath(workspaceID), secrets.GetAppContractValue("affine", "mcp", "path"))
	token := secrets.GetAppSecret("affine", "httpToken")
	require.NotEmpty(t, token, "the minted bearer is published under the contract's secret name")
	assert.True(t, strings.HasPrefix(token, "aff_mcp_v1."), "the published token is AFFiNE's own format")
}

// A second pass must not mint again. AFFiNE reveals the token only at creation
// and keeps every credential it has ever issued, so a configurator that minted
// per reconciliation would leave an unbounded pile of live credentials behind.
func TestEnsureMCPCredential_SteadyStateMintsNothing(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())
	first := secrets.GetAppSecret("affine", "httpToken")
	require.NotEmpty(t, first)

	for i := 0; i < 3; i++ {
		c.ensureMCPCredential(context.Background())
	}

	assert.Equal(t, 1, fake.mintCount(), "a credential that validates is never re-minted")
	assert.Equal(t, first, secrets.GetAppSecret("affine", "httpToken"))
	assert.Len(t, fake.workspaces, 1, "and no extra workspace either")
}

// Revoking the credential in the AFFiNE UI is the case the probe exists for:
// the stored bearer stops validating, so the next pass replaces it rather than
// publishing a dead token forever.
func TestEnsureMCPCredential_ReplacedAfterRevocation(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())
	first := secrets.GetAppSecret("affine", "httpToken")
	require.NotEmpty(t, first)

	fake.mu.Lock()
	delete(fake.validTokens, first)
	fake.mu.Unlock()

	c.ensureMCPCredential(context.Background())

	second := secrets.GetAppSecret("affine", "httpToken")
	assert.NotEqual(t, first, second, "a rejected credential is replaced")
	assert.Equal(t, 2, fake.mintCount())
}

// A workspace the operator already has is adopted instead of a second copy
// being created, and the published path follows it.
func TestEnsureMCPCredential_AdoptsExistingWorkspace(t *testing.T) {
	fake := newFakeAffine("operator-ws")
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())

	assert.Equal(t, []string{"operator-ws"}, fake.workspaces, "no workspace is created beside an existing one")
	assert.Equal(t, "/api/workspaces/operator-ws/mcp", secrets.GetAppContractValue("affine", "mcp", "path"))
}

// Once Bloud has published a workspace, a workspace added later must not move
// the credential: the harness is wired to the published path, and silently
// repointing it would change which library an agent reads.
func TestEnsureMCPCredential_PublishedWorkspaceIsSticky(t *testing.T) {
	fake := newFakeAffine("ws-1")
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())
	published := secrets.GetAppContractValue("affine", "mcp", "path")
	require.NotEmpty(t, published)

	fake.mu.Lock()
	fake.workspaces = append([]string{"newer-first"}, fake.workspaces...)
	fake.mu.Unlock()

	c.ensureMCPCredential(context.Background())
	assert.Equal(t, published, secrets.GetAppContractValue("affine", "mcp", "path"),
		"the previously published workspace is kept even when another sorts first")
}

// A mint that AFFiNE refuses is logged and leaves nothing published, rather
// than half-writing a path with no credential behind it.
func TestEnsureMCPCredential_MintFailurePublishesNoToken(t *testing.T) {
	fake := newFakeAffine("ws-1")
	fake.mintErr = "MCP write tools are not available"
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())

	assert.Empty(t, secrets.GetAppSecret("affine", "httpToken"))
	assert.Equal(t, 0, fake.mintCount())
}

// The GraphQL write must carry the CSRF token the sign-in issued. AFFiNE
// rejects one that does not, so a configurator that skipped it would never
// mint anything.
func TestEnsureMCPCredential_SendsCSRFToken(t *testing.T) {
	fake := newFakeAffine("ws-1")
	fake.requireCSRF = true
	secrets := newStoreSecrets("owner-pass")
	c := mcpConfigurator(t, fake, secrets)

	c.ensureMCPCredential(context.Background())

	assert.Equal(t, 1, fake.mintCount(), "the mint succeeded against a server that demands a CSRF token")
	assert.NotEmpty(t, secrets.GetAppSecret("affine", "httpToken"))
}

// A transport fault must not be read as a revocation: minting a spare
// credential for every blip is the pile-up a long-lived install cannot clean
// up. With a stored token and an unreachable endpoint, nothing new is minted.
func TestMCPProbe_TransientFaultKeepsPublishedCredential(t *testing.T) {
	secrets := newStoreSecrets("owner-pass")
	secrets.secrets["httpToken"] = "aff_mcp_v1.existing.secret"
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = "http://127.0.0.1:1"

	works, rejected, err := c.api.probeMCP(context.Background(), "/api/workspaces/ws-1/mcp", "aff_mcp_v1.existing.secret")
	require.Error(t, err, "an unreachable endpoint is a transport fault, not a verdict")
	assert.False(t, works)
	assert.False(t, rejected)
}

func TestMCPProbe_RejectsDeadToken(t *testing.T) {
	fake := newFakeAffine()
	c := mcpConfigurator(t, fake, newStoreSecrets("p"))

	works, rejected, err := c.api.probeMCP(context.Background(), "/api/workspaces/ws-1/mcp", "aff_mcp_v1.dead.dead")
	require.NoError(t, err)
	assert.False(t, works)
	assert.True(t, rejected, "a 401 is a definitive answer: replace it")
}

func TestMCPProbe_AcceptsLiveToken(t *testing.T) {
	fake := newFakeAffine()
	fake.validTokens["aff_mcp_v1.live.live"] = true
	c := mcpConfigurator(t, fake, newStoreSecrets("p"))

	works, rejected, err := c.api.probeMCP(context.Background(), "/api/workspaces/ws-1/mcp", "aff_mcp_v1.live.live")
	require.NoError(t, err)
	assert.True(t, works)
	assert.False(t, rejected)
}

func TestMCPPath(t *testing.T) {
	assert.Equal(t, "/api/workspaces/abc-123/mcp", mcpPath("abc-123"))
}
