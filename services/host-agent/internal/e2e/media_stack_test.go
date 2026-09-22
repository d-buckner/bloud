// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Media-stack endpoints, published by the catalog to localhost inside the VM
// (each app's metadata.yaml `port`).
var (
	sonarrURL      = getEnvDefault("BLOUD_E2E_SONARR_URL", "http://localhost:8989")
	radarrURL      = getEnvDefault("BLOUD_E2E_RADARR_URL", "http://localhost:7878")
	prowlarrURL    = getEnvDefault("BLOUD_E2E_PROWLARR_URL", "http://localhost:9696")
	qbittorrentURL = getEnvDefault("BLOUD_E2E_QBITTORRENT_URL", "http://localhost:8081")
)

// mediaStackApp is one app of the chain: its catalog id, the deadline for its
// convergence, and the unauthenticated liveness endpoint its instance answers
// once its web host is serving.
type mediaStackApp struct {
	id      string
	timeout time.Duration
	ready   string
}

// mediaStackApps is the PVR chain under test, in install order: every provider
// is RUNNING (and its config.xml written) before the consumer that wires itself
// to it is installed, so each consumer proves its own wiring in the pass that
// installs it. Seerr and Jellyfin are deliberately absent: Seerr's onboarding
// and Jellyfin connection have their own spec and tests, and adding two more
// apps would multiply the runtime budget without adding PVR evidence.
//
// The per-app deadlines are deliberately a sixth of the tier's 30-minute
// `-test.timeout`: the worst case stays inside the budget the whole binary
// shares, and a single-container app plus its image pull converges in seconds;
// a deadline only ever fires on a genuinely stuck install.
var mediaStackApps = []mediaStackApp{
	{id: "qbittorrent", timeout: 6 * time.Minute, ready: qbittorrentURL + "/api/v2/app/version"},
	{id: "sonarr", timeout: 6 * time.Minute, ready: sonarrURL + "/ping"},
	{id: "radarr", timeout: 6 * time.Minute, ready: radarrURL + "/ping"},
	{id: "prowlarr", timeout: 6 * time.Minute, ready: prowlarrURL + "/ping"},
}

// TestMediaStackWiring installs the media stack's PVR chain through the real
// API and asserts every cross-app link through the apps' *own* APIs, never
// through the config files Bloud wrote, which would only prove what was asked
// for, not what the app accepted:
//
//	qbittorrent → sonarr, radarr    download client present and connectable
//	sonarr, radarr → prowlarr       application present and connectable
//	sonarr → /shows, radarr → /movies   root folders present
//
// Each link is polled on its own, so a broken one fails naming itself ("sonarr
// → qbittorrent (download client)") instead of as a generic timeout.
func TestMediaStackWiring(t *testing.T) {
	installMediaStack(t)

	// The test authenticates as a client with each instance's own key, and
	// checks that the PVRs published exactly that key for their consumers: the
	// publication is what Prowlarr and Seerr wire themselves with.
	sonarrKey := apiKey(t, "sonarr")
	radarrKey := apiKey(t, "radarr")
	prowlarrKey := apiKey(t, "prowlarr")
	for appID, key := range map[string]string{"sonarr": sonarrKey, "radarr": radarrKey} {
		if published := publishedAPIKey(t, appID); published != key {
			t.Errorf("%s published %q for its pvr consumers, but its own config.xml holds %q",
				appID, published, key)
		}
	}

	links := []stackLink{
		{
			description: "sonarr → qbittorrent (download client)",
			check:       func() error { return checkDownloadClient(sonarrURL, sonarrKey) },
		},
		{
			description: "radarr → qbittorrent (download client)",
			check:       func() error { return checkDownloadClient(radarrURL, radarrKey) },
		},
		{
			description: "sonarr root folder /shows",
			check:       func() error { return checkRootFolder(sonarrURL, sonarrKey, "/shows") },
		},
		{
			description: "radarr root folder /movies",
			check:       func() error { return checkRootFolder(radarrURL, radarrKey, "/movies") },
		},
		{
			description: "prowlarr → sonarr, radarr (applications)",
			check:       func() error { return checkProwlarrApplications(prowlarrURL, prowlarrKey) },
		},
	}
	for _, link := range links {
		waitForLink(t, 3*time.Minute, link)
	}
}

// installMediaStack installs the chain in provider-first order, waits for each
// app to converge and to answer its own liveness endpoint, and uninstalls
// everything again when the test ends: consumers first, so the providers stay
// up while their consumers are torn down.
func installMediaStack(t *testing.T) {
	t.Helper()
	for _, app := range mediaStackApps {
		postJSON(t, hostAgentURL+"/api/apps/"+app.id+"/install", `{}`, http.StatusAccepted)
		// A fresh VM pulls each image on first install; the chain is four
		// single-container apps, so a per-app deadline is enough.
		waitAppRunning(t, app.id, app.timeout)
		waitHTTPOrFatal(t, 60*time.Second, app.ready)
	}

	t.Cleanup(func() {
		for i := len(mediaStackApps) - 1; i >= 0; i-- {
			id := mediaStackApps[i].id
			if err := postUninstall(id); err != nil {
				t.Errorf("uninstalling %s: %v", id, err)
			}
		}
		if remaining := waitForStackUninstall(4 * time.Minute); remaining != "" {
			t.Errorf("media stack apps still installed after the test: %s", remaining)
		}
	})
}

// waitForStackUninstall waits until no app of the chain is installed and
// returns the ones still listed ("" when the runtime is clean again). It uses
// the *testing-free* fetch path because it runs from a cleanup.
func waitForStackUninstall(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		apps, err := fetchInstalled()
		if err == nil {
			var remaining []string
			for _, want := range mediaStackApps {
				for _, app := range apps {
					if app.CatalogID == want.id {
						remaining = append(remaining, want.id)
					}
				}
			}
			if len(remaining) == 0 {
				return ""
			}
			if time.Now().After(deadline) {
				return strings.Join(remaining, ", ")
			}
		} else if time.Now().After(deadline) {
			return fmt.Sprintf("(could not read installed apps: %v)", err)
		}
		time.Sleep(3 * time.Second)
	}
}

// stackLink is one wiring claim: a description naming the consumer, the
// provider and the surface, plus the check that must eventually hold.
type stackLink struct {
	description string
	check       func() error
}

// waitForLink polls a link until it holds, then logs it. The apps' PostStarts
// run asynchronously with respect to this test (installing a provider re-runs
// its consumers through the orchestrator's staleness path), so every link is
// retried instead of asserted once; a link that never holds is reported with
// the last state its app gave, not as a bare timeout.
func waitForLink(t *testing.T, timeout time.Duration, link stackLink) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := link.check()
		if err == nil {
			t.Logf("verified %s", link.description)
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%s is not wired: %v (still failing after %s)", link.description, err, timeout)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// checkDownloadClient asserts the instance lists a qBittorrent download client
// and that the client actually connects. The list alone would only prove a row
// exists; the API's own test action is what exercises the qBittorrent subnet
// whitelist and the empty credentials Bloud stores.
func checkDownloadClient(baseURL, apiKey string) error {
	var clients []json.RawMessage
	if err := apiGetJSON(baseURL, "/api/v3/downloadclient", apiKey, &clients); err != nil {
		return err
	}
	for _, raw := range clients {
		var entry struct {
			Implementation string `json:"implementation"`
			Name           string `json:"name"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decoding a download client: %w", err)
		}
		if entry.Implementation != "QBittorrent" {
			continue
		}
		status, body, err := apiCall(http.MethodPost, baseURL, "/api/v3/downloadclient/test", apiKey, string(raw))
		if err != nil {
			return fmt.Errorf("testing download client %q: %w", entry.Name, err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("download client test for %q → %d: %s", entry.Name, status, oneLine(body))
		}
		return nil
	}
	return fmt.Errorf("no download client with implementation %q (%d client(s) listed)",
		"QBittorrent", len(clients))
}

// checkRootFolder asserts the instance has registered path as a root folder.
// The mount exists from PreStart on; this is the registration that imports and
// a media server's library lookups rely on.
func checkRootFolder(baseURL, apiKey, path string) error {
	var folders []struct {
		Path string `json:"path"`
	}
	if err := apiGetJSON(baseURL, "/api/v3/rootfolder", apiKey, &folders); err != nil {
		return err
	}
	paths := make([]string, 0, len(folders))
	for _, folder := range folders {
		if folder.Path == path {
			return nil
		}
		paths = append(paths, folder.Path)
	}
	if len(paths) == 0 {
		return fmt.Errorf("root folder %q is not registered (none registered)", path)
	}
	return fmt.Errorf("root folder %q is not registered (registered: %s)", path, strings.Join(paths, ", "))
}

// checkProwlarrApplications asserts Prowlarr syncs into every PVR installed and
// that each configured application connects to its PVR.
func checkProwlarrApplications(baseURL, apiKey string) error {
	var applications []json.RawMessage
	if err := apiGetJSON(baseURL, "/api/v1/applications", apiKey, &applications); err != nil {
		return err
	}

	want := []string{"Sonarr", "Radarr"}
	configured := make(map[string]json.RawMessage, len(want))
	var found []string
	for _, raw := range applications {
		var entry struct {
			Implementation string `json:"implementation"`
			Name           string `json:"name"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("decoding an application: %w", err)
		}
		found = append(found, entry.Implementation)
		for _, name := range want {
			if entry.Implementation == name {
				configured[name] = raw
			}
		}
	}

	var missing []string
	for _, name := range want {
		if _, ok := configured[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("no application with implementation %s (listed: %s)",
			strings.Join(missing, " or "), strings.Join(found, ", "))
	}

	// Each wired application must actually reach its PVR: Prowlarr's test
	// action performs the connect with the API key Bloud published for the PVR
	// and stored in the application document.
	for _, name := range want {
		status, body, err := apiCall(http.MethodPost, baseURL, "/api/v1/applications/test", apiKey, string(configured[name]))
		if err != nil {
			return fmt.Errorf("testing application %s: %w", name, err)
		}
		if status < 200 || status >= 300 {
			return fmt.Errorf("application test for %s → %d: %s", name, status, oneLine(body))
		}
	}
	return nil
}

// apiKey returns an instance's own API key from its config.xml, which every
// Servarr keeps next to its database. The test authenticates as a client, so
// this is its own credential; publishedAPIKey below checks the same value is the
// one the consumers are handed.
func apiKey(t *testing.T, appID string) string {
	t.Helper()
	path := filepath.Join(dataDir(), appID, "config", "config.xml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var cfg struct {
		APIKey string `xml:"ApiKey"`
	}
	if err := xml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	if cfg.APIKey == "" {
		t.Fatalf("%s has no <ApiKey>: %s has not completed a PreStart", path, appID)
	}
	return cfg.APIKey
}

// publishedAPIKey returns the key a consumer is handed for appID through its
// integration binding: what the provider published in the host secret store
// under the secret its contract names.
func publishedAPIKey(t *testing.T, appID string) string {
	t.Helper()
	path := filepath.Join(dataDir(), "secrets.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc struct {
		AppSecrets map[string]struct {
			Published map[string]string `json:"published"`
		} `json:"appSecrets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return doc.AppSecrets[appID].Published["apiKey"]
}

// apiCall performs one request against an app's own API with the instance's
// X-Api-Key. It never fails the test itself: the wiring checks poll, so a
// rejected request is data for the link's error message.
func apiCall(method, baseURL, path, apiKey, body string) (int, []byte, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, baseURL+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	return resp.StatusCode, respBody, err
}

// apiGetJSON reads one of the instance's JSON resources, framing a non-200 as
// an error the calling link can report.
func apiGetJSON(baseURL, path, apiKey string, out any) error {
	status, body, err := apiCall(http.MethodGet, baseURL, path, apiKey, "")
	if err != nil {
		return fmt.Errorf("GET %s%s: %w", baseURL, path, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET %s%s → %d: %s", baseURL, path, status, oneLine(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GET %s%s → %d: decode JSON: %w", baseURL, path, status, err)
	}
	return nil
}

// oneLine renders a response body for an error message: whitespace collapsed,
// bounded, so a link failure stays readable.
func oneLine(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if text == "" {
		return "(empty body)"
	}
	if len(text) > 200 {
		text = text[:200] + "..."
	}
	return text
}
