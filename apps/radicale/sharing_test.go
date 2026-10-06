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

func TestRenderSharesMountsEverySharedCollectionIntoEveryTree(t *testing.T) {
	feeds := []configurator.ICSFeedBinding{feedBinding()}
	got := renderShares([]string{"alice", "bob"}, feeds)

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
	got := renderShares(nil, []configurator.ICSFeedBinding{feedBinding()})

	lines := strings.Split(strings.TrimSpace(got), "\n")
	// Header + the agent's family + the agent's feed.
	assert.Len(t, lines, 3, got)
	assert.Contains(t, got, agentUsername)
}

func TestRenderSharesSkipsIncompleteFeeds(t *testing.T) {
	noKey := feedBinding()
	noKey.APIKey = ""

	got := renderShares([]string{"alice"}, []configurator.ICSFeedBinding{noKey})

	// The feed is not shared, but the family calendar still is: an unready
	// feed says nothing about the collection people write to.
	assert.NotContains(t, got, "radarr")
	assert.Contains(t, got, "/alice/family/")
}

func TestRenderSharesIsDeterministic(t *testing.T) {
	radarr := feedBinding()
	sonarr := feedBinding()
	sonarr.App = "sonarr"

	first := renderShares([]string{"bob", "alice"}, []configurator.ICSFeedBinding{sonarr, radarr})
	second := renderShares([]string{"alice", "bob"}, []configurator.ICSFeedBinding{radarr, sonarr})
	assert.Equal(t, first, second, "a re-render must be byte-identical or every pass restarts Radicale")
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
	assert.Equal(t, []string{"alice", "bob"}, got, "a deactivated account is not somebody's family member")
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
	assert.Equal(t, []string{"alice"}, got)

	// The agent is still in the set that gets the shares, added downstream of
	// the directory rather than coming from it.
	assert.Contains(t, shareRecipients(got), agentUsername)
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
	changed, err := c.syncShares(context.Background(), dataPath, state)
	require.NoError(t, err)
	assert.True(t, changed)

	path := filepath.Join(dataPath, "collections", sharesDirName, sharesFileName)
	first, err := os.ReadFile(path)
	require.NoError(t, err)

	// Put a real share list on disk, then fail the directory read. The file
	// must survive untouched: losing every user's calendars because the
	// provider blinked is worse than a stale list.
	require.NoError(t, os.WriteFile(path, []byte("SENTINEL"), 0o644))
	changed, err = c.syncShares(context.Background(), dataPath, state)
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

func TestEnsureFamilyCalendarCreatesItWhenItIsMissing(t *testing.T) {
	var seen []string
	server := davRecorder(t, http.StatusNotFound, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureFamilyCalendar(context.Background()))

	want := "/" + calendarOwner + "/" + familyCollection + "/"
	assert.Equal(t, []string{"PROPFIND " + want, "MKCALENDAR " + want}, seen)
}

func TestEnsureFamilyCalendarIsANoOpWhenItAlreadyExists(t *testing.T) {
	var seen []string
	server := davRecorder(t, http.StatusMultiStatus, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureFamilyCalendar(context.Background()))
	assert.Equal(t, []string{"PROPFIND /" + calendarOwner + "/" + familyCollection + "/"}, seen,
		"an existing calendar must not be re-created")
}

func TestEnsureFamilyCalendarWithoutACredentialWarnsInsteadOfFailing(t *testing.T) {
	// The Authentik configurator publishes the owner credential during its own
	// convergence. Before that happens the node still has to converge, so a
	// missing credential is a "not yet", not a fault.
	var seen []string
	server := davRecorder(t, http.StatusNotFound, &seen)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = &fakeSecrets{values: map[string]string{}}

	require.NoError(t, c.ensureFamilyCalendar(context.Background()))
	assert.Empty(t, seen, "no credential means no call is made against the server")
}

func TestEnsureFamilyCalendarSurvivesAServerError(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	require.NoError(t, c.ensureFamilyCalendar(context.Background()),
		"a failed family-calendar pass warns and retries next time; it must not park the calendar in ERROR")
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
func TestEnsureFeedCalendarsCreatesEachCollectionNamed(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusNotFound, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	c.ensureFeedCalendars(context.Background(), []configurator.ICSFeedBinding{
		feedWith("radarr", "Movies"),
		feedWith("sonarr", "Shows"),
	})

	assert.Equal(t, []string{
		"PROPFIND /" + calendarOwner + "/Movies/",
		"MKCALENDAR /" + calendarOwner + "/Movies/",
		"PROPFIND /" + calendarOwner + "/Shows/",
		"MKCALENDAR /" + calendarOwner + "/Shows/",
	}, seen)
	assert.Contains(t, bodies["/"+calendarOwner+"/Movies/"], "<D:displayname>Movies</D:displayname>")
	assert.Contains(t, bodies["/"+calendarOwner+"/Shows/"], "<D:displayname>Shows</D:displayname>")
}

func TestEnsureFeedCalendarsIsANoOpWhenTheCollectionExists(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusMultiStatus, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = ownerSecrets()

	c.ensureFeedCalendars(context.Background(), []configurator.ICSFeedBinding{feedWith("radarr", "Movies")})

	assert.Equal(t, []string{"PROPFIND /" + calendarOwner + "/Movies/"}, seen,
		"an existing collection must not be re-created")
	assert.Empty(t, bodies)
}

func TestEnsureFeedCalendarsSkipsAnIncompleteFeed(t *testing.T) {
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

	c.ensureFeedCalendars(context.Background(), []configurator.ICSFeedBinding{noKey, notInstalled})
	assert.Empty(t, seen, "a feed that cannot sync creates nothing")
}

// A server error on one feed must not stop the others: the family calendar and
// every other collection still need their pass, and parking the whole node in
// ERROR because one MKCALENDAR failed is worse than retrying next time.
func TestEnsureFeedCalendarsContinuesPastAServerError(t *testing.T) {
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

	c.ensureFeedCalendars(context.Background(), []configurator.ICSFeedBinding{
		feedWith("radarr", "Movies"),
		feedWith("sonarr", "Shows"),
	})

	assert.Contains(t, seen, "MKCALENDAR /"+calendarOwner+"/Shows/",
		"the second feed still gets created after the first errored")
}

func TestEnsureFeedCalendarsWithoutACredentialMakesNoCalls(t *testing.T) {
	var seen []string
	bodies := map[string]string{}
	server := davBodyRecorder(t, http.StatusNotFound, &seen, &bodies)
	defer server.Close()

	c, _ := newTestConfigurator(t, nil)
	c.baseURL = server.URL
	c.secrets = &fakeSecrets{values: map[string]string{}}

	c.ensureFeedCalendars(context.Background(), []configurator.ICSFeedBinding{feedWith("radarr", "Movies")})
	assert.Empty(t, seen)
}
