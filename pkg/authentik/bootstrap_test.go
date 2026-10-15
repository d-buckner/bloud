// SPDX-License-Identifier: AGPL-3.0-only

package authentik

// These cover the identity bootstrap: the admin account and the API token, which
// the reconciler re-asserts on every convergence pass. The subject is not only
// that the calls are idempotent in effect but that the steady-state path is a
// read, so the tests assert on which HTTP methods the client used, not just on
// the error it returned.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recorded is one call the fake Authentik served.
type recorded struct {
	method string
	path   string
	query  string
}

// stubAuthentik serves the endpoints the identity bootstrap touches and records
// every call. The recording is the point: a test that only checks the returned
// error cannot tell a read from a write, and "it wrote nothing" is the claim
// under test.
type stubAuthentik struct {
	users  string // JSON for core/users results, e.g. "[]" or "[{...}]"
	tokens string // JSON for core/tokens results
	groups string // JSON for core/groups results
	// outposts JSON for outposts/instances results; the list serializer carries
	// config, which is what the embedded-outpost step reads
	outposts string
	key      string // the key view_key reports
	deny     int    // when non-zero, the status to refuse every call with, as an
	// instance that does not recognise our token does

	calls []recorded
}

func (f *stubAuthentik) client(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "test-token")
}

func (f *stubAuthentik) serve(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, recorded{r.Method, r.URL.Path, r.URL.RawQuery})
	if f.deny != 0 {
		w.WriteHeader(f.deny)
		return
	}

	p := r.URL.Path
	switch {
	case p == "/api/v3/core/users/":
		f.list(w, r, f.users, 7)
	case strings.HasPrefix(p, "/api/v3/core/users/"):
		f.usersRoute(w, r, p)
	case p == "/api/v3/core/groups/":
		f.list(w, r, f.groups, 0)
	case strings.HasSuffix(p, "/add_user/"):
		w.WriteHeader(http.StatusNoContent)
	case p == "/api/v3/core/tokens/":
		f.list(w, r, f.tokens, 0)
	case strings.HasPrefix(p, "/api/v3/outposts/instances/"):
		f.outpostsRoute(w, r, p)
	case strings.HasSuffix(p, "/view_key/"):
		writeJSON(w, map[string]string{"key": f.key})
	case strings.HasSuffix(p, "/set_key/"):
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "unexpected: "+r.Method+" "+p, http.StatusInternalServerError)
	}
}

// usersRoute serves the per-user endpoints: set_password, the PATCH that fixes
// email and name, and the group detail read.
func (f *stubAuthentik) usersRoute(w http.ResponseWriter, r *http.Request, p string) {
	switch {
	case strings.HasSuffix(p, "/set_password/"):
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPatch:
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "unexpected: "+r.Method+" "+p, http.StatusInternalServerError)
	}
}

// outpostsRoute serves the outpost list and the full-object write the embedded
// outpost step issues when authentik_host differs.
func (f *stubAuthentik) outpostsRoute(w http.ResponseWriter, r *http.Request, p string) {
	if r.Method == http.MethodPut {
		w.WriteHeader(http.StatusOK)
		return
	}
	f.list(w, r, f.outposts, 0)
}

// list answers a paginated list endpoint, or a create with the given PK.
func (f *stubAuthentik) list(w http.ResponseWriter, r *http.Request, results string, createdPK int) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusCreated, map[string]any{"pk": createdPK})
		return
	}
	if results == "" {
		results = "[]"
	}
	writeJSON(w, json.RawMessage(`{"results":`+results+`}`))
}

func writeJSON(w http.ResponseWriter, v any) { writeJSONStatus(w, http.StatusOK, v) }

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writes returns every non-GET call, which is what a steady-state pass must not
// contain.
func (f *stubAuthentik) writes() []recorded {
	var out []recorded
	for _, c := range f.calls {
		if c.method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

func (f *stubAuthentik) methods() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.method+" "+c.path)
	}
	return out
}

const adminAsSuperuser = `[{"pk":1,"username":"admin","email":"admin@localhost.local","is_superuser":true}]`
const tokenOwnedByAdmin = `[{"identifier":"bloud-api-token","user_obj":{"pk":2,"is_superuser":true}}]`

func TestEnsureAdminUser_LeavesAHealthyAccountAlone(t *testing.T) {
	f := &stubAuthentik{users: adminAsSuperuser}

	require.NoError(t, f.client(t).EnsureAdminUser(context.Background(), "pw", "admin@localhost.local"))

	assert.Empty(t, f.writes(), "an account that already matches must not be written to: the password in particular")
	assert.Len(t, f.calls, 1, "and the whole step is one read")
	assert.Contains(t, f.calls[0].query, "username=admin")
	assert.Contains(t, f.calls[0].query, "include_groups=false")
}

func TestEnsureAdminUser_RepairsTheLegacyEmail(t *testing.T) {
	f := &stubAuthentik{users: `[{"pk":1,"username":"admin","email":"admin@localhost","is_superuser":true}]`}

	require.NoError(t, f.client(t).EnsureAdminUser(context.Background(), "pw", "admin@bloud.example.com"))

	require.Len(t, f.writes(), 1)
	assert.Equal(t, "/api/v3/core/users/1/", f.writes()[0].path)
	assert.Equal(t, http.MethodPatch, f.writes()[0].method)
}

func TestEnsureAdminUser_RestoresSuperuserMembership(t *testing.T) {
	f := &stubAuthentik{
		users:  `[{"pk":1,"username":"admin","email":"a@b.com","is_superuser":false}]`,
		groups: `[{"pk":"g-1","name":"authentik Admins"}]`,
	}

	require.NoError(t, f.client(t).EnsureAdminUser(context.Background(), "pw", "a@b.com"))

	require.Len(t, f.writes(), 1, "membership is the only repair; the password stays the operator's")
	assert.Equal(t, "/api/v3/core/groups/g-1/add_user/", f.writes()[0].path)
}

func TestEnsureAdminUser_CreatesTheAccountWhenAbsent(t *testing.T) {
	f := &stubAuthentik{users: "[]", groups: `[{"pk":"g-1","name":"authentik Admins"}]`}

	require.NoError(t, f.client(t).EnsureAdminUser(context.Background(), "pw", "admin@localhost.local"))

	assert.Equal(t, []string{
		"GET /api/v3/core/users/",
		"GET /api/v3/core/groups/",
		"POST /api/v3/core/users/",
		"POST /api/v3/core/users/7/set_password/",
		"POST /api/v3/core/groups/g-1/add_user/",
	}, f.methods(), "create, then the password the create cannot carry, then the admin group")
}

func TestEnsureAdminUser_NilResultIsNotFoundNotTransportFailure(t *testing.T) {
	f := &stubAuthentik{users: "[]"}

	user, err := f.client(t).lookupUser(context.Background(), "admin")
	require.NoError(t, err)
	assert.Nil(t, user)
}

func TestEnsureAPIToken_ReadOnlyWhenTheKeyMatches(t *testing.T) {
	f := &stubAuthentik{tokens: tokenOwnedByAdmin, key: "the-key"}

	require.NoError(t, f.client(t).EnsureAPIToken(context.Background(), "the-key"))

	assert.Empty(t, f.writes(), "re-imposing a key that is already the key is the no-op this replaced a Django shell with")
	assert.Equal(t, []string{
		"GET /api/v3/core/tokens/",
		"GET /api/v3/core/tokens/bloud-api-token/view_key/",
	}, f.methods())
}

func TestEnsureAPIToken_RewritesOnlyAMismatchedKey(t *testing.T) {
	f := &stubAuthentik{tokens: tokenOwnedByAdmin, key: "someone-elses-key"}

	require.NoError(t, f.client(t).EnsureAPIToken(context.Background(), "the-key"))

	require.Len(t, f.writes(), 1)
	assert.Equal(t, "/api/v3/core/tokens/bloud-api-token/set_key/", f.writes()[0].path)
}

func TestEnsureAPIToken_RestoresLostAdminMembership(t *testing.T) {
	f := &stubAuthentik{
		tokens: `[{"identifier":"bloud-api-token","user_obj":{"pk":2,"is_superuser":false}}]`,
		groups: `[{"pk":"g-1","name":"authentik Admins"}]`,
		key:    "the-key",
	}

	require.NoError(t, f.client(t).EnsureAPIToken(context.Background(), "the-key"))

	assert.Equal(t, "/api/v3/core/groups/g-1/add_user/", f.writes()[0].path,
		"a token whose owner is not an admin is refused for a reason that reads like a bad key")
}

func TestEnsureAPIToken_CreatesServiceAccountAndTokenWhenAbsent(t *testing.T) {
	f := &stubAuthentik{tokens: "[]", groups: `[{"pk":"g-1","name":"authentik Admins"}]`}

	require.NoError(t, f.client(t).EnsureAPIToken(context.Background(), "the-key"))

	assert.Equal(t, []string{
		"GET /api/v3/core/tokens/",
		"GET /api/v3/core/groups/",
		"POST /api/v3/core/users/",
		"POST /api/v3/core/groups/g-1/add_user/",
		"POST /api/v3/core/tokens/",
		"POST /api/v3/core/tokens/bloud-api-token/set_key/",
	}, f.methods(), "the API mints a random key, so set_key is what makes the token the one we authenticate with")
}

// The signal the configurator branches on: a refused token is a result, not a
// failure, and it is what sends the bootstrap to the container's Django shell.
func TestEnsureAPIToken_ReportsAnUnusableToken(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := &stubAuthentik{deny: status}

			err := f.client(t).EnsureAPIToken(context.Background(), "the-key")

			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnauthenticated),
				"the configurator must be able to tell this apart from any other failure")
		})
	}
}

func TestFindGroupID_UsesTheExactNameFilter(t *testing.T) {
	f := &stubAuthentik{groups: `[{"pk":"g-1","name":"authentik Admins"}]`}

	pk, err := f.client(t).findGroupID(context.Background(), "authentik Admins")
	require.NoError(t, err)
	assert.Equal(t, "g-1", pk)
	assert.Contains(t, f.calls[0].query, "name=authentik")
	assert.NotContains(t, f.calls[0].query, "search=")
}

func TestFindGroupID_MissingGroupIsAnError(t *testing.T) {
	f := &stubAuthentik{groups: "[]"}

	_, err := f.client(t).findGroupID(context.Background(), "authentik Admins")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The list serializer carries `users` next to `pk`, so one request answers both
// "which group is this" and "who is already in it". That is what lets a membership
// already in place cost no write.
func TestLookupAdminsGroup_ReadsMembershipWithTheGroup(t *testing.T) {
	f := &stubAuthentik{groups: `[{"pk":"g-1","name":"authentik Admins","users":[7,8]}]`}

	group, err := f.client(t).lookupAdminsGroup(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "g-1", group.PK)
	assert.True(t, group.HasUser(8), "the read must report who is already a member")
	assert.False(t, group.HasUser(9), "HasUser answered true for a user the group does not hold")
	assert.Len(t, f.calls, 1, "resolving a group and its members is one request")
}

func TestLookupAdminsGroup_MissingGroupIsAnError(t *testing.T) {
	f := &stubAuthentik{groups: `[{"pk":"g-1","name":"something else"}]`}

	_, err := f.client(t).lookupAdminsGroup(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// The embedded outpost is the last step of every pass, and its authentik_host
// almost never changes. The list response already serializes config, so a pass
// that has nothing to change is one GET and no write.
func TestEnsureEmbeddedOutpostHost_NoWriteWhenAlreadySet(t *testing.T) {
	f := &stubAuthentik{outposts: `[{"pk":"o-1","name":"authentik Embedded Outpost",` +
		`"config":{"authentik_host":"http://bloud.lan"}}]`}

	require.NoError(t, f.client(t).EnsureEmbeddedOutpostHost(context.Background(), "http://bloud.lan"))
	assert.Empty(t, f.writes(), "a value that already matches must not be written back")
	assert.Len(t, f.calls, 1, "the list response carries config; no second read is needed")
}

func TestEnsureEmbeddedOutpostHost_WritesOnMismatch(t *testing.T) {
	f := &stubAuthentik{outposts: `[{"pk":"o-1","name":"authentik Embedded Outpost",` +
		`"config":{"authentik_host":"http://127.0.0.1:9500"}}]`}

	require.NoError(t, f.client(t).EnsureEmbeddedOutpostHost(context.Background(), "http://bloud.lan"))
	assert.Equal(t, []recorded{{http.MethodGet, "/api/v3/outposts/instances/", "search=Embedded"}}, f.calls[:1])
	assert.Equal(t, http.MethodPut, f.calls[len(f.calls)-1].method)
	assert.Equal(t, "/api/v3/outposts/instances/o-1/", f.calls[len(f.calls)-1].path)
}
