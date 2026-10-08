// SPDX-License-Identifier: AGPL-3.0-only

package radicale

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- rendering ----

// renderWith renders the sharing database for a set of people and feeds, so
// the tests read as the scenario rather than as the plumbing that builds a
// Plan. The names arrive with no directory display name, which is the case
// where the username has to carry the identity alone.
func renderWith(recipients []string, feeds []configurator.ICSFeedBinding) string {
	return renderShares(planCalendars(usersNamed(recipients...), feeds, true, nil))
}

func usersNamed(names ...string) []DirectoryUser {
	out := make([]DirectoryUser, 0, len(names))
	for _, n := range names {
		out = append(out, DirectoryUser{Username: n})
	}
	return out
}

func TestRenderSharesMountsEverySharedCollectionIntoEveryTree(t *testing.T) {
	feeds := []configurator.ICSFeedBinding{feedBinding()}
	got := renderWith([]string{"alice", "bob"}, feeds)

	// The header pins the column order Radicale's csv backend reads.
	assert.True(t, strings.HasPrefix(got, sharesCSVHeader+"\n"))

	// Every person gets the family calendar read-write and the feed read-only.
	for _, user := range []string{"alice", "bob"} {
		assert.Contains(t, got,
			fmt.Sprintf("map;/%s/family/;/%s/family/;none;%s;%s;%s;True;True;False;False;0;0;{};{}\n",
				user, calendarOwner, calendarOwner, user, writableShare),
			"%s should hold the family calendar read-write", user)
		assert.Contains(t, got,
			fmt.Sprintf("map;/%s/Movies/;/%s/Movies/;none;%s;%s;%s;True;True;False;False;0;0;{};{}\n",
				user, calendarOwner, calendarOwner, user, readOnlyShare),
			"%s should hold the feed read-only", user)
	}

	// The agent gets the same set. It is not in the directory listing the
	// people come from, so render adds it: without it `list-calendars` shows
	// the agent nothing at all.
	assert.Contains(t, got,
		fmt.Sprintf("map;/%s/family/;/%s/family/;none;%s;%s;%s;True;True;False;False;0;0;{};{}\n",
			agentUsername, calendarOwner, calendarOwner, agentUsername, writableShare))
	assert.Contains(t, got,
		fmt.Sprintf("map;/%s/Movies/;/%s/Movies/;none;%s;%s;%s;True;True;False;False;0;0;{};{}\n",
			agentUsername, calendarOwner, calendarOwner, agentUsername, readOnlyShare))
}

func TestRenderSharesWithNoUsersStillSharesWithTheAgent(t *testing.T) {
	got := renderWith(nil, []configurator.ICSFeedBinding{feedBinding()})

	lines := strings.Split(strings.TrimSpace(got), "\n")
	// Header + the agent's family + the agent's feed.
	assert.Len(t, lines, 3, got)
	assert.Contains(t, got, agentUsername)
}

func TestRenderSharesSkipsIncompleteFeeds(t *testing.T) {
	noKey := feedBinding()
	noKey.APIKey = ""

	got := renderWith([]string{"alice"}, []configurator.ICSFeedBinding{noKey})

	// The feed is not shared, but the family calendar still is: an unready
	// feed says nothing about the collection people write to.
	assert.NotContains(t, got, "radarr")
	assert.Contains(t, got, "/alice/family/")
}

func TestRenderSharesIsDeterministic(t *testing.T) {
	radarr := feedBinding()
	sonarr := feedBinding()
	sonarr.App = "sonarr"

	first := renderWith([]string{"bob", "alice"}, []configurator.ICSFeedBinding{sonarr, radarr})
	second := renderWith([]string{"alice", "bob"}, []configurator.ICSFeedBinding{radarr, sonarr})
	assert.Equal(t, first, second, "a re-render must be byte-identical or every pass restarts Radicale")
}

// TestRenderSharesMatchesThePrePlanGoldenOutput is the Phase 1 gate. The
// golden bytes were captured from the renderer that this abstraction replaced --
// the one that walked the `familyCollection` constant and the feed bindings
// directly -- for three recipients and two feeds. The plan-driven renderer must
// produce exactly those bytes.
//
// This is the whole reason to do the refactor before adding anything: it proves
// the abstraction is faithful against a set that is already known good. If a
// single byte moves, the refactor changed behavior, and a changed sharing.csv
// means a Radicale restart on the pass that ships it.
func TestRenderSharesMatchesThePrePlanGoldenOutput(t *testing.T) {
	const golden = "ShareType;PathOrToken;PathMapped;Conversion;Owner;User;Permissions;EnabledByOwner;EnabledByUser;HiddenByOwner;HiddenByUser;TimestampCreated;TimestampUpdated;Properties;Actions\n" +
		"map;/alice/family/;/calendar-service/family/;none;calendar-service;alice;RWrw;True;True;False;False;0;0;{};{}\n" +
		"map;/alice/Movies/;/calendar-service/Movies/;none;calendar-service;alice;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/alice/Shows/;/calendar-service/Shows/;none;calendar-service;alice;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/bob/family/;/calendar-service/family/;none;calendar-service;bob;RWrw;True;True;False;False;0;0;{};{}\n" +
		"map;/bob/Movies/;/calendar-service/Movies/;none;calendar-service;bob;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/bob/Shows/;/calendar-service/Shows/;none;calendar-service;bob;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/caldav-service/family/;/calendar-service/family/;none;calendar-service;caldav-service;RWrw;True;True;False;False;0;0;{};{}\n" +
		"map;/caldav-service/Movies/;/calendar-service/Movies/;none;calendar-service;caldav-service;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/caldav-service/Shows/;/calendar-service/Shows/;none;calendar-service;caldav-service;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/carol/family/;/calendar-service/family/;none;calendar-service;carol;RWrw;True;True;False;False;0;0;{};{}\n" +
		"map;/carol/Movies/;/calendar-service/Movies/;none;calendar-service;carol;Rr;True;True;False;False;0;0;{};{}\n" +
		"map;/carol/Shows/;/calendar-service/Shows/;none;calendar-service;carol;Rr;True;True;False;False;0;0;{};{}\n"

	feeds := []configurator.ICSFeedBinding{
		{
			ProviderRef:  configurator.ProviderRef{App: "sonarr", Installed: true, BaseURL: "http://sonarr:8989"},
			APIKey:       "sonarr-key",
			Path:         "/api/v3/calendar",
			CalendarName: "Shows",
		},
		{
			ProviderRef:  configurator.ProviderRef{App: "radarr", Installed: true, BaseURL: "http://radarr:7878"},
			APIKey:       "radarr-key",
			Path:         "/api/v3/calendar",
			CalendarName: "Movies",
		},
	}

	// Deliberately unsorted on the way in: the render must not depend on it.
	got := renderWith([]string{"carol", "alice", "bob"}, feeds)
	assert.Equal(t, golden, withoutPersonalRows(got),
		"the family and feed rows must be the exact bytes the constant-driven renderer emitted")
}

// withoutPersonalRows drops the rows the per-user calendars add, leaving the
// family and feed rows exactly as the Phase 1 golden captured them.
//
// The match is on the mapped-to field, which is the only field in a row that
// can carry the people prefix. A row's own mount is `/bob/Personal/` and its
// owner is a bare name, so nothing else in the line can produce this substring.
func withoutPersonalRows(rendered string) string {
	var kept []string
	for _, line := range strings.SplitAfter(rendered, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, ";/"+calendarOwner+"/"+personalPrefix) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "")
}

func TestShareRecipientsExcludesTheOwnerAndDedupes(t *testing.T) {
	got := shareRecipients([]string{"alice", "alice", calendarOwner, " ", agentUsername, "bob"})
	assert.Equal(t, []string{"alice", "bob", agentUsername}, got)
}

// ---- user enumeration ----

// authentikDirectory serves the three calls ListUsers makes: the internal user
// page, the admin group search, and the admin group membership.
func authentikDirectory(t *testing.T, users []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v3/core/users/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"results": users})
		case strings.HasPrefix(r.URL.Path, "/api/v3/core/groups/") && r.URL.RawQuery != "":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{{"pk": "1", "name": "authentik Admins"}},
			})
		case strings.HasPrefix(r.URL.Path, "/api/v3/core/groups/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"users": []int{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func ssoState(t *testing.T, baseURL string) *configurator.AppState {
	t.Helper()
	return &configurator.AppState{
		DataPath: t.TempDir(),
		Integrations: configurator.Integrations{
			SSO: []configurator.SSOBinding{{
				ProviderRef: configurator.ProviderRef{
					Kind:      configurator.ProviderKindApp,
					App:       "authentik",
					Installed: true,
					LocalURL:  baseURL,
				},
				APIToken: "directory-token",
			}},
		},
	}
}

func TestCalendarRecipientsListsActiveUsers(t *testing.T) {
	server := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "is_active": true, "type": "internal"},
		{"pk": 2, "username": "bob", "is_active": true, "type": "internal"},
		{"pk": 3, "username": "cousin", "is_active": false, "type": "internal"},
	})
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	got, ok := c.calendarRecipients(context.Background(), ssoState(t, server.URL))
	require.True(t, ok)
	assert.Equal(t, []string{"alice", "bob"}, usernamesOf(got),
		"a deactivated account is not somebody's family member")
}

func TestCalendarRecipientsExcludesTheServiceAccounts(t *testing.T) {
	server := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "is_active": true, "type": "internal"},
		{"pk": 2, "username": authentik.CalendarServiceUsername, "is_active": true, "type": "service_account"},
		{"pk": 3, "username": authentik.CalDAVServiceUsername, "is_active": true, "type": "service_account"},
	})
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	got, ok := c.calendarRecipients(context.Background(), ssoState(t, server.URL))
	require.True(t, ok)
	assert.Equal(t, []string{"alice"}, usernamesOf(got))

	// The agent is still in the set that gets the shares, added downstream of
	// the directory rather than coming from it.
	assert.Contains(t, shareRecipients(usernamesOf(got)), agentUsername)
}

// The human name has to survive the read: it becomes the displayname on that
// person's calendar, which is the string a household member reads when the
// agent shows them whose calendar they are looking at.
func TestCalendarRecipientsCarriesTheDirectoryDisplayName(t *testing.T) {
	server := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "name": "Alice Hart", "is_active": true, "type": "internal"},
		{"pk": 2, "username": "bob", "name": "", "is_active": true, "type": "internal"},
	})
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	got, ok := c.calendarRecipients(context.Background(), ssoState(t, server.URL))
	require.True(t, ok)
	require.Len(t, got, 2)

	assert.Equal(t, "Alice Hart", got[0].DisplayName)
	assert.Empty(t, got[1].DisplayName, "a blank name stays blank rather than becoming whitespace")
	assert.Equal(t, "Bob's calendar", personalDisplayName(got[1]),
		"with no human name the username carries the identity")
	assert.Equal(t, "Alice Hart's calendar", personalDisplayName(got[0]))
}

// A username is the identity a personal calendar is keyed on and goes straight
// into a collection path, so one that cannot be a path segment is not a person
// this feature can provision.
func TestCalendarRecipientsSkipsAUsernameThatCannotBeAPathSegment(t *testing.T) {
	server := authentikDirectory(t, []map[string]any{
		{"pk": 1, "username": "alice", "is_active": true, "type": "internal"},
		{"pk": 2, "username": "evil/../x", "is_active": true, "type": "internal"},
		{"pk": 3, "username": ".hidden", "is_active": true, "type": "internal"},
		{"pk": 4, "username": "two words", "is_active": true, "type": "internal"},
	})
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	got, ok := c.calendarRecipients(context.Background(), ssoState(t, server.URL))
	require.True(t, ok)
	assert.Equal(t, []string{"alice"}, usernamesOf(got),
		"a username that escapes the person- prefix or hides its directory is not provisioned")
}

func TestCalendarRecipientsReportsFailureWhenTheDirectoryIsUnreachable(t *testing.T) {
	c, _ := newTestConfigurator(t, nil)
	// A port nothing listens on: the call fails, and the bool must say so
	// rather than returning an empty list that reads as "nobody to share with".
	_, ok := c.calendarRecipients(context.Background(), ssoState(t, "http://127.0.0.1:1"))
	assert.False(t, ok)
}

func TestCalendarRecipientsWithoutAnIdentityProviderReportsFailure(t *testing.T) {
	c, _ := newTestConfigurator(t, nil)
	_, ok := c.calendarRecipients(context.Background(), appState(t.TempDir(), ldapOutput()))
	assert.False(t, ok, "no token means the directory cannot be read, which is not the same as an empty family")
}

// ---- the write path keeps the last good list ----

func TestSyncSharesKeepsTheExistingFileWhenTheDirectoryCannotBeRead(t *testing.T) {
	c, dataPath := newTestConfigurator(t, nil)
	state := appState(dataPath, ldapOutput())

	// First pass: no directory, no file on disk, so a header-only database is
	// written rather than leaving the sharing backend with nothing to read.
	changed, err := c.syncShares(dataPath, c.planFor(context.Background(), state))
	require.NoError(t, err)
	assert.True(t, changed)

	path := filepath.Join(dataPath, "collections", sharesDirName, sharesFileName)
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	// Put a real share list on disk, then fail the directory read. The file
	// must survive untouched: losing every user's calendars because the
	// provider blinked is worse than a stale list.
	require.NoError(t, os.WriteFile(path, []byte("SENTINEL"), 0o644))
	changed, err = c.syncShares(dataPath, c.planFor(context.Background(), state))
	require.NoError(t, err)
	assert.False(t, changed)

	second, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "SENTINEL", string(second), "an unreadable directory must not rewrite the shares")

	// And the earlier header-only write really was the header.
	assert.True(t, strings.HasPrefix(string(first), sharesCSVHeader+"\n"))
}

// ---- the family calendar ----

type fakeSecrets struct {
	values map[string]string
}

func (f *fakeSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *fakeSecrets) GetAppSecret(app, key string) string             { return f.values[app+"/"+key] }
func (f *fakeSecrets) SetAppSecret(string, string, string) error       { return nil }
func (f *fakeSecrets) SetAppContractValue(string, string, string, string) error {
	return nil
}
func (f *fakeSecrets) GetAppContractValue(string, string, string) string { return "" }

// davRecorder answers the DAV calls ensureFamilyCalendar makes and records what
// arrived, so the test can assert on the verb the server actually saw.
func davRecorder(t *testing.T, propfindStatus int, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "PROPFIND":
			w.WriteHeader(propfindStatus)
		case "MKCALENDAR":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

func ownerSecrets() *fakeSecrets {
	return &fakeSecrets{values: map[string]string{
		appName + "/" + authentik.CalendarOwnerSecretKey: "owner-secret",
	}}
}

func TestEnsureCalendarsCreatesTheFamilyCalendarWhenItIsMissing(t *testing.T) {
	var seen []string
	server := davRecorder(t, http.StatusNotFound, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, nil, true, nil)))

	want := "/" + calendarOwner + "/" + familyCollection + "/"
	assert.Equal(t, []string{"PROPFIND " + want, "MKCALENDAR " + want}, seen)
}

func TestEnsureCalendarsIsANoOpWhenItAlreadyExists(t *testing.T) {
	var seen []string
	server := davRecorder(t, http.StatusMultiStatus, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, nil, true, nil)))
	assert.Equal(t, []string{"PROPFIND /" + calendarOwner + "/" + familyCollection + "/"}, seen,
		"an existing calendar must not be re-created")
}

func TestEnsureCalendarsWithoutACredentialWarnsInsteadOfFailing(t *testing.T) {
	// The Authentik configurator publishes the owner credential during its own
	// convergence. Before that happens the node still has to converge, so a
	// missing credential is a "not yet", not a fault.
	var seen []string
	server := davRecorder(t, http.StatusNotFound, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = &fakeSecrets{values: map[string]string{}}

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, nil, true, nil)))
	assert.Empty(t, seen, "no credential means no call is made against the server")
}

func TestEnsureCalendarsSurvivesAServerError(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, nil, true, nil)),
		"a failed create pass warns and retries next time; it must not park the calendar in ERROR")
	assert.Equal(t, []string{"PROPFIND"}, seen)
}

// ---- feed calendar pre-creation ----

// davBodyRecorder answers the DAV calls the collection-creation path makes and
// records the verb, the path, and the MKCALENDAR body, because the display
// name is carried in the body and nowhere else.
func davBodyRecorder(t *testing.T, propfindStatus int, seen *[]string, bodies *map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*seen = append(*seen, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "PROPFIND":
			w.WriteHeader(propfindStatus)
		case "MKCALENDAR":
			body := make([]byte, r.ContentLength)
			_, _ = io.ReadFull(r.Body, body)
			(*bodies)[r.URL.Path] = string(body)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

func feedWith(name, calendarName string) configurator.ICSFeedBinding {
	f := feedBinding()
	f.App = name
	f.Node = "apps-" + name
	f.BaseURL = "http://apps-" + name + ":7878"
	f.CalendarName = calendarName
	return f
}

// The collection has to be created by Bloud rather than left to the sync
// sidecar, and it has to carry the provider's declared name as its display
// name. pimsync cannot do either: a webcal source has no display name to
// sync, and a collection Radicale makes for itself derives one from the path,
// which is how `calendar-service/radarr` reached a family member's calendar.
func TestEnsureCalendarsCreatesEachFeedCollectionNamed(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusNotFound, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, []configurator.ICSFeedBinding{
		feedWith("radarr", "Movies"),
		feedWith("sonarr", "Shows"),
	}, true, nil)))

	// The family calendar is in the plan too, and it is created first: one loop
	// over one value rather than a bespoke pass per kind.
	assert.Equal(t, []string{
		"PROPFIND /" + calendarOwner + "/family/",
		"MKCALENDAR /" + calendarOwner + "/family/",
		"PROPFIND /" + calendarOwner + "/Movies/",
		"MKCALENDAR /" + calendarOwner + "/Movies/",
		"PROPFIND /" + calendarOwner + "/Shows/",
		"MKCALENDAR /" + calendarOwner + "/Shows/",
	}, seen)
	assert.Contains(t, bodies["/"+calendarOwner+"/Movies/"], "<D:displayname>Movies</D:displayname>")
	assert.Contains(t, bodies["/"+calendarOwner+"/Shows/"], "<D:displayname>Shows</D:displayname>")
}

func TestEnsureCalendarsIsANoOpWhenTheFeedCollectionExists(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusMultiStatus, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, []configurator.ICSFeedBinding{feedWith("radarr", "Movies")}, true, nil)))

	assert.Equal(t, []string{
		"PROPFIND /" + calendarOwner + "/family/",
		"PROPFIND /" + calendarOwner + "/Movies/",
	}, seen, "an existing collection must not be re-created")
	assert.Empty(t, bodies)
}

func TestEnsureCalendarsSkipsAnIncompleteFeed(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusNotFound, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	noKey := feedWith("radarr", "Movies")
	noKey.APIKey = ""
	notInstalled := feedWith("sonarr", "Shows")
	notInstalled.Installed = false

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, []configurator.ICSFeedBinding{noKey, notInstalled}, true, nil)))

	// The feeds create nothing; the family calendar is unaffected, because an
	// unready feed says nothing about the collection people write to.
	assert.Equal(t, []string{
		"PROPFIND /" + calendarOwner + "/family/",
		"MKCALENDAR /" + calendarOwner + "/family/",
	}, seen, "a feed that cannot sync creates nothing")
}

// A server error on one calendar must not stop the others: the remaining
// collections still need their pass, and parking the whole node in ERROR
// because one MKCALENDAR failed is worse than retrying next time.
func TestEnsureCalendarsContinuesPastAServerError(t *testing.T) {
	var seen []string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.Method {
		case "PROPFIND":
			if calls == 0 {
				calls++
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		case "MKCALENDAR":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, []configurator.ICSFeedBinding{
		feedWith("radarr", "Movies"),
		feedWith("sonarr", "Shows"),
	}, true, nil)))

	assert.Contains(t, seen, "MKCALENDAR /"+calendarOwner+"/Shows/",
		"the later calendars still get created after an earlier one errored")
}

func TestEnsureCalendarsWithoutACredentialMakesNoCalls(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusNotFound, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = &fakeSecrets{values: map[string]string{}}

	require.NoError(t, c.ensureCalendars(context.Background(), planCalendars(nil, []configurator.ICSFeedBinding{feedWith("radarr", "Movies")}, true, nil)))
	assert.Empty(t, seen)
}

// ---- per-user calendars ----

// parsedRow is a rendered share line, for assertions that read as the
// relationship between the columns rather than as positional indexing.
type parsedRow struct {
	mountedAt  string
	mappedTo   string
	user       string
	perms      string
	properties string
}

func parseRows(rendered string) []parsedRow {
	var out []parsedRow
	for _, line := range strings.Split(strings.TrimSuffix(rendered, "\n"), "\n") {
		fields := strings.Split(line, ";")
		if len(fields) < 15 || fields[0] == "ShareType" {
			continue
		}
		out = append(out, parsedRow{
			mountedAt:  fields[1],
			mappedTo:   fields[2],
			user:       fields[5],
			perms:      fields[6],
			properties: fields[13],
		})
	}
	return out
}

func household() []DirectoryUser {
	return []DirectoryUser{
		{Username: "alice", DisplayName: "Alice Hart"},
		{Username: "bob", DisplayName: "Bob Nunes"},
		{Username: "carol"},
	}
}

func TestPlanEmitsOnePersonalCalendarPerPerson(t *testing.T) {
	plan := planCalendars(household(), nil, true, nil)

	for _, want := range []struct{ segment, display string }{
		{"person-alice", "Alice Hart's calendar"},
		{"person-bob", "Bob Nunes's calendar"},
		{"person-carol", "Carol's calendar"},
	} {
		var found *Calendar
		for i := range plan.Calendars {
			if plan.Calendars[i].Segment == want.segment {
				found = &plan.Calendars[i]
			}
		}
		require.NotNil(t, found, "no calendar planned for %s", want.segment)
		assert.Equal(t, want.display, found.DisplayName)
		require.Len(t, found.Grants, 2, "one mount for the person, one for the agent")
	}
}

// The person's own mount reads "Personal" rather than their own name, and the
// agent's mount carries the name. Same collection, two readings, because the
// mount is where the personal-versus-shared distinction should show.
func TestPersonalMountsCarryTheirOwnDisplayNames(t *testing.T) {
	plan := planCalendars([]DirectoryUser{{Username: "bob", DisplayName: "Bob Nunes"}}, nil, true, nil)
	rows := parseRows(renderShares(plan))

	var own, agent *parsedRow
	for i := range rows {
		switch rows[i].user {
		case "bob":
			if rows[i].mountedAt == "/bob/Personal/" {
				own = &rows[i]
			}
		case agentUsername:
			if rows[i].mountedAt == "/"+agentUsername+"/person-bob/" {
				agent = &rows[i]
			}
		}
	}

	require.NotNil(t, own, "bob has no Personal mount")
	assert.Equal(t, "/calendar-service/person-bob/", own.mappedTo)
	assert.Equal(t, writableShare, own.perms)
	assert.Equal(t, "{'D:displayname': 'Personal'}", own.properties)

	require.NotNil(t, agent, "the agent has no mount for bob")
	assert.Equal(t, "/calendar-service/person-bob/", agent.mappedTo)
	assert.Equal(t, writableShare, agent.perms)
	assert.Equal(t, "{}", agent.properties,
		"the agent inherits the collection's own displayname, which carries the person's name")
}

// Decision 4 in the plan: assert this rather than trust it. A row that mounts
// one person's personal collection into a second person's tree is a privacy
// bug with a named victim, and nothing in the rendered file looks wrong about
// it unless this is checked.
func TestPersonalCalendarsAreNeverSharedWithAnotherPerson(t *testing.T) {
	plan := planCalendars(household(), []configurator.ICSFeedBinding{feedBinding()}, true, nil)

	for _, row := range parseRows(renderShares(plan)) {
		trimmed := strings.TrimPrefix(row.mappedTo, "/"+calendarOwner+"/"+personalPrefix)
		if trimmed == row.mappedTo {
			continue // not a personal collection
		}
		owner := strings.TrimSuffix(trimmed, "/")
		if row.user == agentUsername {
			continue
		}
		assert.Equal(t, owner, row.user,
			"a personal collection is mounted for someone other than its owner: %s -> %s",
			row.mappedTo, row.mountedAt)
	}
}

// The mount path is checked before the grant is issued. A map share shadows
// whatever already sits at that path, so a person who made a calendar called
// Personal themselves would lose sight of it to a provisioning step they
// never asked for.
func TestOwnMountIsSkippedWhenThePersonAlreadyHasOne(t *testing.T) {
	dataPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataPath, "collections", "bob", personalMount), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataPath, "collections", "bob", personalMount, ".Radicale.props"), []byte("{}"), 0o644))

	c, _ := newTestConfigurator(t, nil)
	occupied := c.occupiedMounts(dataPath, household())
	plan := planCalendars(household(), nil, true, occupied)

	require.Len(t, plan.Conflicts, 1)
	assert.Equal(t, "bob", plan.Conflicts[0].Recipient)
	assert.Equal(t, personalMount, plan.Conflicts[0].Mount)

	for _, row := range parseRows(renderShares(plan)) {
		assert.NotEqual(t, "/bob/Personal/", row.mountedAt,
			"the shadowing mount must not be rendered")
	}

	// The agent keeps its mount: the collision is about the person's own tree,
	// and their calendar is still reachable by the agent and by themselves
	// under whatever they named it.
	var agentSeesBob bool
	for _, row := range parseRows(renderShares(plan)) {
		if row.user == agentUsername && row.mappedTo == "/calendar-service/person-bob/" {
			agentSeesBob = true
		}
	}
	assert.True(t, agentSeesBob, "a collision in the person's tree must not remove the agent's mount")
}

// A directory that is not a collection shadows nothing, so it must not be
// mistaken for one and cost the person their mount.
func TestOccupiedMountsIgnoresADirectoryThatIsNotACollection(t *testing.T) {
	dataPath := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dataPath, "collections", "bob", personalMount), 0o755))

	c, _ := newTestConfigurator(t, nil)
	assert.Empty(t, c.occupiedMounts(dataPath, household()),
		"without .Radicale.props there is no collection there")
}

// Adding a person must add only their rows. If a new user reordered anything
// else, the file changes wholesale and Radicale restarts on every directory
// edit.
func TestAddingAPersonChangesOnlyTheirRows(t *testing.T) {
	before := parseRows(renderShares(planCalendars(household(), nil, true, nil)))
	after := parseRows(renderShares(planCalendars(
		append(household(), DirectoryUser{Username: "dave"}), nil, true, nil)))

	added := func(rows []parsedRow, user string) []parsedRow {
		var out []parsedRow
		for _, r := range rows {
			if r.mappedTo == "/"+calendarOwner+"/"+personalPrefix+user+"/" {
				out = append(out, r)
			}
		}
		return out
	}
	assert.Len(t, added(after, "dave"), 2, "the new person gets their own mount and the agent's")
	assert.Subset(t, after, before, "nothing that was already there changed")
	// Three rows, not two: joining the household also puts the new person in the
	// family calendar's audience.
	assert.Len(t, after, len(before)+3, "exactly the new person's rows were added")
}
