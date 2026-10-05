// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCalendarAggregation is the end-to-end proof of the calendar story: a
// Radarr and a Sonarr each publish an ICS feed, Radicale's vendored storage
// plugin subscribes to both server-side, and the events land in collections
// under the operator's own DAV account where any CalDAV client (Calino,
// AFFiNE, Thunderbird) sees them.
//
// It exercises the real product path: install intents, the graph ordering
// Radicale after its feed providers, PreStart rendering ics_sync.json, and the
// PostStart resync that re-renders it and restarts Radicale when the providers
// appear after it is already RUNNING.
func TestCalendarAggregation(t *testing.T) {
	// The synced collections live under the account of the user who completed
	// first-run setup. The integration runtime has no browser wizard, so this
	// performs the same first-run POST the e2e lifecycle does.
	const operator = "e2eoperator"
	const operatorPassword = "e2eoperator123"
	createFirstRunOperator(t, operator, operatorPassword)

	// Radicale first: its PreStart runs before any feed provider exists, so the
	// aggregation has to be picked up by the PostStart resync after the feeds
	// are installed. That ordering is the bug this test exists to catch.
	postJSON(t, hostAgentURL+"/api/apps/radicale/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "radicale", 10*time.Minute)

	for _, app := range []string{"sonarr", "radarr"} {
		postJSON(t, hostAgentURL+"/api/apps/"+app+"/install", `{}`, http.StatusAccepted)
		waitAppRunning(t, app, 6*time.Minute)
	}

	t.Cleanup(func() {
		for _, app := range []string{"radarr", "sonarr", "radicale"} {
			if err := postUninstall(app); err != nil {
				t.Errorf("uninstalling %s: %v", app, err)
			}
		}
	})

	// The feed endpoints must answer a calendar, authenticated by the same key
	// the providers published under the icsFeed contract. A 401 here means the
	// key the binding carried is not the one the app accepts.
	waitFeedServesCalendar(t, "radarr", "http://localhost:7878", "/feed/v3/calendar/Radarr.ics")
	waitFeedServesCalendar(t, "sonarr", "http://localhost:8989", "/feed/v3/calendar/Sonarr.ics")

	// The resync re-renders ics_sync.json and restarts Radicale, so the jobs
	// appear a pass or two after the providers converge.
	waitForFeedJobs(t, operator)

	// The plugin's first sync creates the collections (even an empty feed still
	// proves the fetch → parse → collection-create chain ran). PROPFIND Depth 1
	// on the operator's tree must list both calendars, which is the exact shape
	// a CalDAV client reads.
	waitForSyncedCollections(t, operator, operatorPassword)

	// The whole family sees the same thing. A second Bloud user, created after
	// Radicale converged, must find the feeds and the family calendar mounted
	// in their own tree once the resync picks them up.
	assertSharedWithASecondUser(t)
}

// assertSharedWithASecondUser is the behavioral half of #215. Everything above
// proves the feeds sync in; this proves they arrive in somebody else's tree.
//
// The second user is created through the admin API after Radicale is already
// RUNNING, which is the case a static share list would miss: the recipient set
// is read from the identity provider on the resync, not captured at install.
func assertSharedWithASecondUser(t *testing.T) {
	t.Helper()

	const (
		fellow     = "e2efamily"
		fellowPass = "e2efamily123"
	)
	createManagedUser(t, fellow, fellowPass)

	waitForShareRows(t, fellow)

	// The recipient's own home listing is exactly what a calendar client reads.
	// Depth 1 on /<user>/ must show the family calendar and both feeds, which
	// is the whole promise: add one CalDAV account, inherit the household.
	res, listing := davRequestDepth(t, "PROPFIND", "/"+fellow+"/", fellow, fellowPass, "1")
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND /%s/ = %d, want 207\n%s", fellow, res.StatusCode, listing)
	}
	for _, want := range []string{"family", "radarr", "sonarr"} {
		if !strings.Contains(listing, fellow+"/"+want) {
			t.Errorf("shared collection %q is missing from /%s/:\n%s", want, fellow, listing)
		}
	}

	assertFamilyCalendarIsWritable(t, fellow, fellowPass)
	assertFeedIsReadOnly(t, fellow, fellowPass)
}

// createManagedUser adds a Bloud user through the admin API. The integration
// runtime reaches the agent over loopback, which is the trusted position the
// admin router accepts, so no session is needed.
func createManagedUser(t *testing.T, username, password string) {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q,"role":"member"}`, username, password)
	resp := agentPost(t, hostAgentURL+"/api/admin/users", "application/json", strings.NewReader(body))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusConflict {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /api/admin/users for %q = %d: %s", username, resp.StatusCode, data)
	}
	t.Logf("created the second Bloud user %q", username)
}

// readSharingCSV reads the sharing database the configurator owns.
func readSharingCSV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(appDataDir("radicale"), "collections", "sharing.csv")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// waitForShareRows polls until the sharing database carries the recipient's
// family mount and both feed mounts. The recipient set is re-read on the
// PostStart resync, so a user added after the last pass shows up one cycle
// later, and the resync restarts the container on the change.
func waitForShareRows(t *testing.T, recipient string) {
	t.Helper()
	want := []string{
		"map;/" + recipient + "/family/;",
		"map;/" + recipient + "/radarr/;",
		"map;/" + recipient + "/sonarr/;",
	}
	deadline := time.Now().Add(6 * time.Minute)
	for {
		csv := readSharingCSV(t)
		missing := make([]string, 0, len(want))
		for _, w := range want {
			if !strings.Contains(csv, w) {
				missing = append(missing, w)
			}
		}
		if len(missing) == 0 {
			t.Logf("sharing.csv carries the family mounts for %q", recipient)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("sharing.csv never carried %v for %q; have:\n%s", missing, recipient, csv)
		}
		time.Sleep(5 * time.Second)
	}
}

// assertFamilyCalendarIsWritable puts an event into the shared family
// calendar through the recipient's mount. A read-write map share that cannot
// actually be written is the failure this asserts against, and it is the thing
// that makes the family calendar a calendar rather than another feed.
func assertFamilyCalendarIsWritable(t *testing.T, user, password string) {
	t.Helper()
	href := fmt.Sprintf("bloud-family-%d.ics", time.Now().UnixNano())
	path := "/" + user + "/family/" + href

	res, body := davPut(t, path, user, password, icsEvent("bloud-family-"+href, "Family test event"))
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s = %d, want 201; the family share is not writable\n%s", path, res.StatusCode, body)
	}

	res, listing := davRequestDepth(t, "PROPFIND", "/"+user+"/family/", user, password, "1")
	if res.StatusCode != http.StatusMultiStatus || !strings.Contains(listing, href) {
		t.Errorf("the event is not listed in %s (status %d):\n%s", path, res.StatusCode, listing)
	}
}

// assertFeedIsReadOnly keeps the write grant above honest: the feeds are
// projections of someone else's system, and a user writing into one would be
// overwritten on the next sync anyway. Better refused now than silently lost.
func assertFeedIsReadOnly(t *testing.T, user, password string) {
	t.Helper()
	href := fmt.Sprintf("bloud-should-not-land-%d.ics", time.Now().UnixNano())
	path := "/" + user + "/radarr/" + href

	res, body := davPut(t, path, user, password, icsEvent("bloud-should-not-land-"+href, "Must not persist"))
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusMethodNotAllowed ||
		res.StatusCode == http.StatusUnauthorized {
		return
	}
	t.Fatalf("PUT %s = %d, want a refusal; the read-only feed share is writable\n%s", path, res.StatusCode, body)
}

// icsEvent renders a minimal single-event iCalendar document.
func icsEvent(uid, summary string) string {
	return "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//Bloud//e2e//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260101T000000Z\r\n" +
		"DTSTART:20260101T100000Z\r\n" +
		"DTEND:20260101T110000Z\r\n" +
		"SUMMARY:" + summary + "\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
}

// davPut uploads a calendar item with Basic credentials.
func davPut(t *testing.T, path, user, password, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("PUT", radicaleURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build PUT %s: %v", path, err)
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "text/calendar")
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirected; a DAV request must not be bounced to a login page")
		},
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		t.Fatalf("read PUT %s body: %v", path, err)
	}
	return res, string(raw)
}

// createFirstRunOperator performs the first-run setup POST the browser wizard
// would. The setup endpoints are public until the first user exists, so this
// uses a plain client rather than the admin one. Idempotent across runs: if a
// user already exists, the existing one is the operator.
func createFirstRunOperator(t *testing.T, username, password string) {
	t.Helper()

	resp, err := http.Get(hostAgentURL + "/api/setup/status")
	if err != nil {
		t.Fatalf("GET /api/setup/status: %v", err)
	}
	var status struct {
		SetupRequired bool `json:"setupRequired"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		resp.Body.Close()
		t.Fatalf("decode setup status: %v", err)
	}
	resp.Body.Close()
	if !status.SetupRequired {
		t.Logf("first-run setup already completed; reusing the existing operator")
		return
	}

	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	resp, err = http.Post(hostAgentURL+"/api/setup/create-user", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/setup/create-user: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create-user = %d: %s", resp.StatusCode, data)
	}
	t.Logf("created first-run operator %q", username)
}

// waitFeedServesCalendar polls a Servarr feed until it answers text/calendar
// with the published API key. The key is read from the host secret store, the
// same value the icsFeed binding carries.
func waitFeedServesCalendar(t *testing.T, appID, baseURL, feedPath string) {
	t.Helper()
	key := publishedAPIKey(t, appID)
	if key == "" {
		t.Fatalf("%s published no apiKey", appID)
	}

	url := baseURL + feedPath + "?apikey=" + key
	deadline := time.Now().Add(3 * time.Minute)
	for {
		resp, err := http.Get(url)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && strings.Contains(resp.Header.Get("Content-Type"), "text/calendar") {
				t.Logf("%s feed serves a calendar (%d bytes)", appID, len(body))
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s feed %s never served text/calendar (last err %v)", appID, url, err)
		}
		time.Sleep(5 * time.Second)
	}
}

// icsSyncJob is one entry of Radicale's ics_sync.json, the config the vendored
// plugin reads. Only the fields this test asserts are modeled.
type icsSyncJob struct {
	Feed       string `json:"feed"`
	Collection string `json:"collection"`
}

// readICSSyncJobs parses Radicale's rendered ics_sync.json.
func readICSSyncJobs(t *testing.T) []icsSyncJob {
	t.Helper()
	path := filepath.Join(appDataDir("radicale"), "config", "ics_sync.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var jobs []icsSyncJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return jobs
}

// waitForFeedJobs polls until Radicale's rendered sync config carries one job
// for Radarr and one for Sonarr, both under the operator's account.
func waitForFeedJobs(t *testing.T, operator string) {
	t.Helper()
	want := map[string]string{
		"radarr": operator + "/radarr",
		"sonarr": operator + "/sonarr",
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		jobs := readICSSyncJobs(t)
		found := map[string]bool{}
		for _, job := range jobs {
			for app, collection := range want {
				if job.Collection == collection {
					found[app] = true
				}
			}
		}
		if found["radarr"] && found["sonarr"] {
			t.Logf("ics_sync.json carries %d feed job(s)", len(jobs))
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ics_sync.json never carried both feeds (have %d job(s): %+v)", len(jobs), jobs)
		}
		time.Sleep(5 * time.Second)
	}
}

// waitForSyncedCollections polls the operator's DAV tree until the plugin has
// created the two synced calendars. The listing is a Depth 1 PROPFIND, which is
// what a calendar client performs to enumerate a user's calendars.
func waitForSyncedCollections(t *testing.T, operator, password string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		res, body := davRequestDepth(t, "PROPFIND", "/"+operator+"/", operator, password, "1")
		if res.StatusCode == http.StatusMultiStatus &&
			strings.Contains(body, operator+"/radarr") &&
			strings.Contains(body, operator+"/sonarr") {
			t.Log("Radarr and Sonarr collections appear in the operator's DAV tree")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("synced collections never appeared under /%s/ (last status %d):\n%s",
				operator, res.StatusCode, body)
		}
		time.Sleep(5 * time.Second)
	}
}
