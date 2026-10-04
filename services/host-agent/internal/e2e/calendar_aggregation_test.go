// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"encoding/json"
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
