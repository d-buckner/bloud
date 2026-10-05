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
// Radarr and a Sonarr each publish an ICS feed, Radicale subscribes to both
// server-side, and the events land in collections owned by the shared
// calendar account and mounted into every Bloud user's tree, where any
// CalDAV client (Calino, AFFiNE, Thunderbird) sees them.
//
// It exercises the real product path: install intents, the graph ordering
// Radicale after its feed providers, PreStart rendering the sidecar's
// pimsync.conf, and the PostStart resync that re-renders it when the
// providers appear after Radicale is already RUNNING.
func TestCalendarAggregation(t *testing.T) {
	// The first-run operator is created because the runtime has no browser
	// wizard, not because the synced collections belong to them. They live
	// under calendar-service; the operator sees them the same way everybody
	// else does, as a mount in their own tree.
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
		// Twelve minutes, not the six a warm cache needs. The linuxserver
		// images are a few hundred MB and a cold pull on a shared runner took
		// over five on its own, which put a six-minute budget inside the
		// pull's own shadow: the test would fail for the runner's network
		// rather than for anything Bloud did.
		waitAppRunning(t, app, 12*time.Minute)
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

	// The resync re-renders the sidecar's config, so the pairs appear a pass or
	// two after the providers converge.
	waitForFeedJobs(t, "calendar-service")

	// The sidecar's first sync creates the collections (even an empty feed
	// still proves the fetch → parse → collection-create chain ran). PROPFIND
	// Depth 1
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

	// A new recipient changes sharing.csv, and a changed sharing.csv restarts
	// Radicale. The app row says running before the container is answering
	// again, so the first DAV call after a share change lands on a socket that
	// is mid-teardown. Wait for the server rather than the row.
	waitForDAVReady(t, "/"+fellow+"/", fellow, fellowPass)

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

// davTry performs a DAV request without failing the test on a transport
// error, so a caller can retry through a container restart.
func davTry(method, path, user, pass, body string) (int, string, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, radicaleURL+path, reader)
	if err != nil {
		return 0, "", err
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	if body != "" {
		req.Header.Set("Content-Type", "text/calendar")
	} else {
		req.Header.Set("Depth", "1")
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirected; a DAV request must not be bounced to a login page")
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return res.StatusCode, "", err
	}
	return res.StatusCode, string(raw), nil
}

// waitForDAVReady blocks until Radicale answers a request at path instead of
// resetting the connection. It exists because the thing a test can check
// first (the app row) is not the thing that is ready first (the socket): a
// share or config change restarts the container while the orchestrator has
// already promoted the node.
func waitForDAVReady(t *testing.T, path, user, pass string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	var lastErr error
	for {
		status, _, err := davTry("PROPFIND", path, user, pass, "")
		if err == nil && status != 0 {
			return
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("Radicale never started answering %s: %v", path, lastErr)
		}
		time.Sleep(5 * time.Second)
	}
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

// readSharingCSV reads the sharing database the configurator owns. Radicale's
// csv backend lives in a `collection-db` directory under the storage tree,
// not in the tree root.
func readSharingCSV(t *testing.T) string {
	t.Helper()
	path := filepath.Join(appDataDir("radicale"), "collections", "collection-db", "sharing.csv")
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

	status, body := davPutRetry(t, path, user, password, icsEvent("bloud-family-"+href, "Family test event"))
	if status != http.StatusCreated && status != http.StatusNoContent {
		t.Fatalf("PUT %s = %d, want 201; the family share is not writable\n%s", path, status, body)
	}

	status, listing := davGetRetry(t, "/"+user+"/family/", user, password)
	if status != http.StatusMultiStatus || !strings.Contains(listing, href) {
		t.Errorf("the event is not listed in %s (status %d):\n%s", path, status, listing)
	}
}

// assertFeedIsReadOnly keeps the write grant above honest: the feeds are
// projections of someone else's system, and a user writing into one would be
// overwritten on the next sync anyway. Better refused now than silently lost.
func assertFeedIsReadOnly(t *testing.T, user, password string) {
	t.Helper()
	href := fmt.Sprintf("bloud-should-not-land-%d.ics", time.Now().UnixNano())
	path := "/" + user + "/radarr/" + href

	status, body := davPutRetry(t, path, user, password, icsEvent("bloud-should-not-land-"+href, "Must not persist"))
	if status == http.StatusForbidden || status == http.StatusMethodNotAllowed ||
		status == http.StatusUnauthorized {
		return
	}
	t.Fatalf("PUT %s = %d, want a refusal; the read-only feed share is writable\n%s", path, status, body)
}

// davPutRetry writes a calendar item, retrying a transport failure. A restart
// that lands between two assertions is not a statement about the permission
// model, so it gets retried rather than reported.
func davPutRetry(t *testing.T, path, user, password, body string) (int, string) {
	t.Helper()
	return davRetry(t, "PUT", path, user, password, body)
}

// davGetRetry lists a collection, retrying a transport failure.
func davGetRetry(t *testing.T, path, user, password string) (int, string) {
	t.Helper()
	return davRetry(t, "PROPFIND", path, user, password, "")
}

// davRetry keeps trying until the server answers. The deadline is generous
// because the alternative is a test that fails on a restart it caused itself.
func davRetry(t *testing.T, method, path, user, password, body string) (int, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	var lastErr error
	for {
		status, respBody, err := davTry(method, path, user, password, body)
		if err == nil {
			return status, respBody
		}
		lastErr = err
		if time.Now().After(deadline) {
			t.Fatalf("%s %s never reached Radicale: %v", method, path, lastErr)
		}
		time.Sleep(5 * time.Second)
	}
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

// pimsyncPair is one `pair` block from the rendered pimsync.conf. The config
// is scfg, a flat `key value` format, so this reads the block by hand rather
// than pulling in a parser for a format with no Go library.
type pimsyncPair struct {
	name     string
	storageA string
	storageB string
	oneWay   bool
}

// readPimsyncConf returns the sidecar's rendered config from the app's own
// data tree, or empty when it is not there yet. The file is written by the
// sidecar's PreStart, so its contents are the evidence that the pass ran and
// knew about the feeds.
func readPimsyncConf(t *testing.T) string {
	t.Helper()
	path := filepath.Join(appDataDir("radicale"), "pimsync", "pimsync.conf")
	data, err := os.ReadFile(path)
	if err != nil {
		// A missing file is not a failure here: the sidecar's first pass may not
		// have run yet, and the caller polls.
		t.Logf("pimsync config not readable yet: %v", err)
		return ""
	}
	return string(data)
}

// pimsyncTargetURL is the `url` of the CalDAV target storage block. That path
// is where ownership of the synced collections lives: a target under
// /calendar-service/ means the feeds land in the shared account's tree, not
// in whoever claimed the instance first.
func pimsyncTargetURL(t *testing.T) string {
	t.Helper()
	inTarget := false
	for _, line := range strings.Split(readPimsyncConf(t), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "storage ") && strings.HasSuffix(trimmed, "{"):
			inTarget = strings.HasPrefix(trimmed, "storage bloud {")
		case trimmed == "}":
			inTarget = false
		case inTarget && strings.HasPrefix(trimmed, "url "):
			return strings.Trim(strings.TrimPrefix(trimmed, "url "), `"`)
		}
	}
	return ""
}

// readPimsyncPairs parses the `pair` blocks out of the sidecar's config.
func readPimsyncPairs(t *testing.T) []pimsyncPair {
	t.Helper()
	var pairs []pimsyncPair
	var cur *pimsyncPair
	for _, line := range strings.Split(readPimsyncConf(t), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "pair ") && strings.HasSuffix(trimmed, "{"):
			name := strings.TrimSuffix(strings.TrimPrefix(trimmed, "pair "), "{")
			cur = &pimsyncPair{name: strings.TrimSpace(name)}
		case cur == nil:
			continue
		case trimmed == "}":
			pairs = append(pairs, *cur)
			cur = nil
		case strings.HasPrefix(trimmed, "storage_a "):
			cur.storageA = strings.TrimPrefix(trimmed, "storage_a ")
		case strings.HasPrefix(trimmed, "storage_b "):
			cur.storageB = strings.TrimPrefix(trimmed, "storage_b ")
		case trimmed == "one_way":
			cur.oneWay = true
		}
	}
	return pairs
}

// waitForFeedJobs polls until the sidecar's rendered config carries one
// one-way pair per provider, each pushing into the shared owner's tree. It is
// the operator's successor as the argument: the feeds are no longer synced
// into whoever claimed the instance first, they are synced into the account
// that owns them and shared out from there.
func waitForFeedJobs(t *testing.T, owner string) {
	t.Helper()
	want := []string{"radarr", "sonarr"}
	found := map[string]bool{}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		target := pimsyncTargetURL(t)
		if !strings.Contains(target, "/"+owner+"/") {
			t.Logf("pimsync target %q does not name owner %q yet", target, owner)
		} else {
			for _, pair := range readPimsyncPairs(t) {
				if !contains(want, pair.name) || found[pair.name] {
					continue
				}
				if pair.storageA != pair.name || pair.storageB != "bloud" || !pair.oneWay {
					t.Fatalf("pimsync pair %q has the wrong shape: storage_a=%q storage_b=%q one_way=%v",
						pair.name, pair.storageA, pair.storageB, pair.oneWay)
				}
				found[pair.name] = true
				t.Logf("pimsync pair %s: %s -> %s (%s), one_way",
					pair.name, pair.storageA, pair.storageB, target)
			}
		}
		if found["radarr"] && found["sonarr"] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pimsync.conf never carried both one-way feed pairs into %q (found: %v)", owner, found)
		}
		time.Sleep(5 * time.Second)
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// waitForSyncedCollections polls the recipient's DAV tree until the synced
// calendars appear in it. The listing is a Depth 1 PROPFIND, which is what a
// calendar client performs to enumerate a user's calendars.
//
// It retries through a transport failure on purpose. The pass that adds the
// feed jobs restarts Radicale, and the node is promoted before the container
// is answering again, so a single PROPFIND here can land on a socket that is
// mid-teardown. That is a restart the test caused itself, not a missing
// collection.
func waitForSyncedCollections(t *testing.T, user, password string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	var lastStatus int
	var lastBody string
	for {
		status, body := davGetRetry(t, "/"+user+"/", user, password)
		lastStatus, lastBody = status, body
		if status == http.StatusMultiStatus &&
			strings.Contains(body, user+"/radarr") &&
			strings.Contains(body, user+"/sonarr") {
			t.Log("Radarr and Sonarr collections appear in the recipient's DAV tree")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("synced collections never appeared under /%s/ (last status %d):\n%s",
				user, lastStatus, lastBody)
		}
		time.Sleep(5 * time.Second)
	}
}
