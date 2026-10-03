// SPDX-License-Identifier: AGPL-3.0-only

package affine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// graphqlMembers answers the shared-workspace membership surface of the fake:
// the member list query and the invite mutation.
func (f *fakeAffine) graphqlMembers(w http.ResponseWriter, query string, variables map[string]any) bool {
	switch {
	case strings.Contains(query, "members(take: $take)"):
		writeGraphQLData(w, map[string]any{
			"workspace": map[string]any{"members": f.members},
		})
		return true

	case strings.Contains(query, "inviteMembers("):
		if f.inviteErr != "" {
			writeGraphQLErr(w, f.inviteErr)
			return true
		}
		emails, _ := variables["emails"].([]any)
		results := make([]map[string]any, 0, len(emails))
		for i, e := range emails {
			email, _ := e.(string)
			f.invitedEmails = append(f.invitedEmails, email)
			// AFFiNE's member list unions active members with outstanding
			// invitations, so a successful invite makes the address present
			// right away. Modelling that here is what lets a test run a further
			// pass and assert the same address was not invited twice.
			f.members = append(f.members, map[string]string{"email": email, "status": "Pending"})
			results = append(results, map[string]any{
				"email":    email,
				"inviteId": "perm-invite-" + string(rune('a'+i)),
				"error":    nil,
			})
		}
		f.inviteCreates++
		writeGraphQLData(w, map[string]any{"inviteMembers": results})
		return true
	}
	return false
}

// fakeAuthentik is a stand-in for the identity provider's user directory. It
// counts directory reads, which is what a steady-state pass must not repeat.
type fakeAuthentik struct {
	users  []map[string]any
	groups map[string][]int
	reads  int
	server *httptest.Server
}

func (f *fakeAuthentik) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v3/core/users/":
		f.reads++
		_ = json.NewEncoder(w).Encode(map[string]any{"results": f.users})
	case "/api/v3/core/groups/":
		out := make([]map[string]string, 0, len(f.groups))
		for id := range f.groups {
			out = append(out, map[string]string{"pk": id, "name": "authentik Admins"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
	default:
		// /api/v3/core/groups/<id>/
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		groupID := parts[len(parts)-1]
		_ = json.NewEncoder(w).Encode(map[string]any{"users": f.groups[groupID]})
	}
}

// url is the address the fake directory listens on, for wiring into a binding.
func (f *fakeAuthentik) url() string { return f.server.URL }

// membersState builds an AppState whose `sso` binding carries an API token and
// points its host-side address at the fake directory.
func membersState(t *testing.T, idpURL string) *configurator.AppState {
	t.Helper()
	st := &configurator.AppState{DataPath: t.TempDir()}
	st.Integrations.SSO = []configurator.SSOBinding{{
		ProviderRef: configurator.ProviderRef{
			App:      "authentik",
			Node:     "apps-authentik-server",
			Port:     9001,
			BaseURL:  "http://apps-authentik-server:9001",
			LocalURL: idpURL,
		},
		APIToken: "idp-token",
	}}
	return st
}

func startFakeAuthentik(t *testing.T, users []map[string]any) *fakeAuthentik {
	t.Helper()
	f := &fakeAuthentik{users: users, groups: map[string][]int{"g1": {1}}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	f.server = srv
	return f
}

func TestBloudUserEmails(t *testing.T) {
	users := []authentik.ManagedUserInfo{
		{Username: "alice", Email: "Alice@Example.com", IsActive: true},
		{Username: "bob", Email: "bob@example.com", IsActive: true},
		{Username: "carol", Email: "carol@example.com", IsActive: false},
		{Username: "dave", Email: "", IsActive: true},
		{Username: "erin", Email: "   ", IsActive: true},
		{Username: "frank", Email: "alice@example.com", IsActive: true},
	}
	assert.Equal(t, []string{"alice@example.com", "bob@example.com"}, bloudUserEmails(users),
		"inactive accounts and addressless ones are dropped, the rest deduped and sorted")
}

func TestBloudUserEmailsEmpty(t *testing.T) {
	assert.Empty(t, bloudUserEmails(nil))
}

func TestSharedIdentityRequiresAToken(t *testing.T) {
	_, ok := sharedIdentity(nil)
	assert.False(t, ok, "a nil state has no identity provider")

	st := &configurator.AppState{}
	st.Integrations.SSO = []configurator.SSOBinding{{ProviderRef: configurator.ProviderRef{App: "authentik"}}}
	_, ok = sharedIdentity(st)
	assert.False(t, ok, "a binding with no published token is not a usable identity provider")

	st.Integrations.SSO = []configurator.SSOBinding{{
		ProviderRef: configurator.ProviderRef{App: "authentik"},
		APIToken:    "t",
	}}
	got, ok := sharedIdentity(st)
	assert.True(t, ok)
	assert.Equal(t, "authentik", got.App)
}

// The first pass invites every Bloud user the workspace does not already know,
// and the invitation reaches AFFiNE as an address, not a user id: the two
// systems only share the address.
func TestEnsureSharedMembers_InvitesMissingUsers(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 1, "username": "admin", "email": "admin@localhost.local", "is_active": true, "type": "internal"},
		{"pk": 2, "username": "alice", "email": "alice@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	fake.members = []map[string]string{{"email": "admin@localhost.local", "status": "Accepted"}}
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))

	c.ensureSharedMembers(context.Background(), membersState(t, idp.url()))

	assert.Equal(t, []string{"alice@localhost.local"}, fake.invitedEmails,
		"only the user the workspace does not already have gets invited")
}

// A user who was invited and has not accepted yet shows up in the member list as
// a Pending row. Re-inviting them every pass would pile up invitations, so the
// diff must treat any status as presence.
func TestEnsureSharedMembers_PendingInvitationIsNotReinvited(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 1, "username": "alice", "email": "alice@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	fake.members = []map[string]string{{"email": "alice@localhost.local", "status": "Pending"}}
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))

	c.ensureSharedMembers(context.Background(), membersState(t, idp.url()))

	assert.Empty(t, fake.invitedEmails, "a pending invitation is presence")
	assert.Equal(t, 0, fake.inviteCreates)
}

// Steady state must be reads only. A pass that re-invited would grow the
// invitation table on the ~60s reconciliation timer.
func TestEnsureSharedMembers_SteadyStateInvitesNothing(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 1, "username": "admin", "email": "admin@localhost.local", "is_active": true, "type": "internal"},
		{"pk": 2, "username": "alice", "email": "alice@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	fake.members = []map[string]string{
		{"email": "admin@localhost.local", "status": "Accepted"},
		{"email": "alice@localhost.local", "status": "Accepted"},
	}
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))

	for i := 0; i < 3; i++ {
		c.ensureSharedMembers(context.Background(), membersState(t, idp.url()))
	}
	assert.Empty(t, fake.invitedEmails)
	assert.Equal(t, 0, fake.inviteCreates)
}

// With no token published there is no way to ask who the Bloud users are. That
// is "not ready", and the pass must invite nobody rather than guessing.
func TestEnsureSharedMembers_NoTokenInvitesNobody(t *testing.T) {
	fake := newFakeAffine("ws-1")
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))
	st := &configurator.AppState{DataPath: t.TempDir()}
	st.Integrations.SSO = []configurator.SSOBinding{{ProviderRef: configurator.ProviderRef{App: "authentik"}}}

	c.ensureSharedMembers(context.Background(), st)

	assert.Empty(t, fake.invitedEmails)
	assert.Equal(t, 0, fake.inviteCreates)
}

// A directory that cannot be read must not be mistaken for an empty Bloud, which
// would be a silent no-op rather than a reported failure.
func TestEnsureSharedMembers_DirectoryFailureInvitesNobody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	fake := newFakeAffine("ws-1")
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))

	c.ensureSharedMembers(context.Background(), membersState(t, srv.URL))

	assert.Empty(t, fake.invitedEmails)
	assert.Equal(t, 0, fake.inviteCreates)
}

// A failed invite call is swallowed rather than failing the node, but it must not
// be recorded as a success either.
func TestEnsureSharedMembers_InviteFailureIsSwallowed(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 2, "username": "alice", "email": "alice@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	fake.inviteErr = "quota exceeded"
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))

	require.NotPanics(t, func() {
		c.ensureSharedMembers(context.Background(), membersState(t, idp.url()))
	})
	assert.Equal(t, 0, fake.inviteCreates)
	assert.Empty(t, fake.invitedEmails)
}

// The reported bug: a user created in Settings after AFFiNE was installed must
// reach the shared workspace without anyone restarting anything. The membership
// pass is a diff against the directory, so a later pass that finds one more
// account invites exactly that account, and the pass after that finds nothing.
func TestEnsureSharedMembers_NewUserIsInvitedOnALaterPass(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 1, "username": "admin", "email": "admin@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	fake.members = []map[string]string{{"email": "admin@localhost.local", "status": "Accepted"}}
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))
	st := membersState(t, idp.url())

	c.ensureSharedMembers(context.Background(), st)
	require.Empty(t, fake.invitedEmails, "the establishing pass finds everyone already in")

	// The operator adds a user in Settings. Nothing restarts and no intent is
	// raised: the next pass simply reads a directory with one more row in it.
	idp.users = append(idp.users, map[string]any{
		"pk": 2, "username": "alice", "email": "alice@localhost.local",
		"is_active": true, "type": "internal",
	})
	c.ensureSharedMembers(context.Background(), st)

	assert.Equal(t, []string{"alice@localhost.local"}, fake.invitedEmails,
		"the user added after the install is invited by the next pass")

	// And the invitation lands as a Pending row, so the pass after that is a
	// no-op rather than a second invitation for the same person.
	c.ensureSharedMembers(context.Background(), st)
	assert.Equal(t, 1, fake.inviteCreates,
		"the pending invitation is presence: nobody gets invited twice")
}

// The directory call must leave the host on the host-side address. BaseURL is a
// container-network name the host-agent process cannot resolve, so using it would
// make every membership pass fail on a working instance.
func TestEnsureSharedMembers_UsesTheHostVantagePoint(t *testing.T) {
	idp := startFakeAuthentik(t, []map[string]any{
		{"pk": 2, "username": "alice", "email": "alice@localhost.local", "is_active": true, "type": "internal"},
	})
	fake := newFakeAffine("ws-1")
	c := inferenceConfigurator(t, fake, newStoreSecrets("owner-pass"))
	st := membersState(t, idp.url())

	c.ensureSharedMembers(context.Background(), st)

	assert.Equal(t, 1, idp.reads, "the directory was reached at the LocalURL the binding carried")
	assert.Equal(t, []string{"alice@localhost.local"}, fake.invitedEmails)
}
