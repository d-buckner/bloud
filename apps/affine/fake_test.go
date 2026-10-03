// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// storeSecrets is an in-memory AppSecretsProvider that actually keeps what the
// configurator publishes, so the workspace flow's read-modify-publish cycle
// (adopt the published workspace, republish its id) can be exercised across
// passes.
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

// fakeAffine is a stand-in for the slice of AFFiNE the configurator touches:
// the owner sign-in that issues the session and CSRF cookies, the GraphQL
// surface for workspace listing and creation, and the BYOK and membership
// surfaces the AI wiring and the shared-workspace pass use.
//
// It records calls so a test can assert what the configurator did rather than
// only what it ended up storing: creating a workspace on every reconciliation
// pass, or inviting a member twice, are leaks the stored value alone cannot
// show.
type fakeAffine struct {
	mu sync.Mutex

	workspaces []string
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

	// --- Shared workspace membership state ---

	// members is the workspace's member list in the shape the members query
	// returns it. It holds active members and outstanding invitations together,
	// which is how AFFiNE actually answers, so a diff test models the real thing
	// rather than two separate lists.
	members []map[string]string
	// inviteCreates counts inviteMembers calls that succeeded.
	inviteCreates int
	// invitedEmails accumulates every address the fake was asked to invite, so a
	// test can assert the same address was not invited twice across passes.
	invitedEmails []string
	// inviteErr, when set, makes every inviteMembers call fail with a GraphQL
	// error envelope.
	inviteErr string
}

func newFakeAffine(workspaces ...string) *fakeAffine {
	return &fakeAffine{workspaces: workspaces}
}

func (f *fakeAffine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/auth/sign-in":
		http.SetCookie(w, &http.Cookie{Name: "affine_session", Value: "sess-1", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "csrf-1", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"u1","email":"bloud-admin@affine.localhost"}`))

	case "/graphql":
		f.graphql(w, r)

	default:
		http.NotFound(w, r)
	}
}

// graphqlRequest is one GraphQL call as it arrives on the wire. The fake
// decodes both the plain JSON body and the multipart `operations` field into
// the same shape.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// decodeGraphQLRequest reads a GraphQL call from either a JSON body or a
// multipart upload. It writes the error response itself and reports false
// when the request never became a query, so the handler's own switch stays
// about queries rather than about transport.
func (f *fakeAffine) decodeGraphQLRequest(w http.ResponseWriter, r *http.Request) (graphqlRequest, bool) {
	if f.requireCSRF && r.Header.Get("x-csrf-token") == "" {
		writeGraphQLErr(w, "missing csrf token")
		return graphqlRequest{}, false
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		return f.decodeMultipartGraphQL(w, r)
	}
	var req graphqlRequest
	if err := newDecoder(r).Decode(&req); err != nil {
		writeGraphQLErr(w, "bad request")
		return graphqlRequest{}, false
	}
	return req, true
}

// decodeMultipartGraphQL reads the query out of the `operations` form field
// of a graphql-multipart-request-spec upload; the file itself stays in the
// parts for whichever case consumes it.
func (f *fakeAffine) decodeMultipartGraphQL(w http.ResponseWriter, r *http.Request) (graphqlRequest, bool) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeGraphQLErr(w, "bad multipart request")
		return graphqlRequest{}, false
	}
	var req graphqlRequest
	if err := json.Unmarshal([]byte(r.FormValue("operations")), &req); err != nil {
		writeGraphQLErr(w, "bad operations field")
		return graphqlRequest{}, false
	}
	return req, true
}

func (f *fakeAffine) graphql(w http.ResponseWriter, r *http.Request) {
	req, ok := f.decodeGraphQLRequest(w, r)
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	// The BYOK surface is handled separately so this switch stays under the
	// cyclop gate as the fake grows with the app's API.
	if f.graphqlByok(w, req.Query, req.Variables) {
		return
	}
	// Same for the shared-workspace membership surface.
	if f.graphqlMembers(w, req.Query, req.Variables) {
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

// apiConfigurator wires a configurator to a fake AFFiNE and a recording store.
func apiConfigurator(t *testing.T, fake *fakeAffine, secrets *storeSecrets) *Configurator {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})
	c.baseURL = server.URL
	return c
}

// The appApi publication is what lets a wrapper that cannot join the identity
// provider sign in to AFFiNE: the owner username as a value, the owner password
// as the secret. Both are the same account the configurator itself signs in
// with, so a wrapper reaches exactly the instance Bloud bootstrapped.
func TestPublishAppAPICredential_PublishesOwnerUsernameAndPassword(t *testing.T) {
	secrets := newStoreSecrets("owner-pass")
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})

	c.publishAppAPICredential()

	assert.Equal(t, "owner-pass", secrets.GetAppSecret("affine", "password"))
	assert.Equal(t, c.adminEmail, secrets.GetAppContractValue("affine", "appApi", "username"))
}

// A missing secrets store must not panic the provider: the node is the knowledge
// base itself, and an add-on credential that cannot be published is a warning,
// not a failure.
func TestPublishAppAPICredential_NilSecretsSafe(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	assert.NotPanics(t, c.publishAppAPICredential)
}
