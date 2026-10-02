// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// radicaleURL is the app's published port inside the VM.
var radicaleURL = getEnvDefault("BLOUD_E2E_RADICALE_URL", "http://localhost:5232")

// radicaleConfigPath is the INI file the configurator writes and the container
// is pointed at with --config.
func radicaleConfigPath() string {
	return filepath.Join(dataDir(), "radicale", "config", "config")
}

// davRequest issues a DAV method with Basic credentials. Redirects are refused
// rather than followed: a 302 in front of a calendar sync is exactly the
// failure mode the ldap strategy was chosen to avoid, so following one would
// hide it.
func davRequest(t *testing.T, method, path, user, pass string) (*http.Response, string) {
	t.Helper()
	return davRequestDepth(t, method, path, user, pass, "")
}

// davRequestDepth is davRequest with an explicit DAV Depth header. Radicale
// reads a missing header as Depth 0 (radicale/app/propfind.py:
// `environ.get("HTTP_DEPTH", "0")`), so it answers with the requested
// collection alone and never with its children. A listing that has to show
// what is inside a collection must ask for Depth 1: RFC 4918 lets a server
// treat an absent header as infinity, and this one does not.
func davRequestDepth(t *testing.T, method, path, user, pass, depth string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, radicaleURL+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	if depth != "" {
		req.Header.Set("Depth", depth)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirected; a DAV request must not be bounced to a login page")
		},
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s %s body: %v", method, path, err)
	}
	return res, string(body)
}

// TestRadicaleInstallViaAPI is the rung every other test in this file stands
// on: it puts Radicale through the real install path (intent queue, container
// creation, PreStart writing the INI and the ldap-secret, PostStart, route
// generation) and waits for the orchestrator to converge it.
//
// It has to be the first test here. TestMain wipes user apps before the suite
// runs and nothing else in the package installs Radicale, so without this the
// rest of the file waits on an app that was never installed and each one times
// out with `last status ""`.
func TestRadicaleInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/radicale/install", `{}`, http.StatusAccepted)
	// Radicale is one container with no bundled database, so this is a single
	// image pull. The budget matches the install rung of the other single-
	// container apps, which is sized for a cold VM rather than for the app.
	waitAppRunning(t, "radicale", 10*time.Minute)
}

// TestRadicaleConfigScopesTheLDAPSearch pins the two settings that decide
// whether anyone can sign in at all.
//
// Both were found by breaking a live install. Authentik's LDAP serves the
// username in sAMAccountName and cn but not in uid, where it is a content
// hash; and its virtual-groups tree carries the same names as the user tree,
// so a search from the directory base resolves every login name to two
// entries, which Radicale rejects as ambiguous. A filter that is wrong in
// either way still starts cleanly and serves 401 forever, which reads like a
// broken password rather than a broken config.
func TestRadicaleConfigScopesTheLDAPSearch(t *testing.T) {
	path := radicaleConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config %s: %v", path, err)
	}
	cfg := string(raw)

	wants := []string{
		"type = ldap",
		"ldap_base = ou=users," + expectedLDAPBaseDN,
		"ldap_filter = (sAMAccountName={0})",
		"ldap_reader_dn = " + expectedLDAPBindUser,
		"ldap_ignore_attribute_create_modify_timestamp = true",
		"type = owner_only",
	}
	for _, w := range wants {
		if !strings.Contains(cfg, w) {
			t.Errorf("generated config is missing %q\n---\n%s", w, cfg)
		}
	}

	// The reader credential is referenced by file, never inlined: this file is
	// the artifact an operator pastes into a bug report.
	if strings.Contains(cfg, readSecrets(t).LdapBindPassword) {
		t.Error("the LDAP reader secret must not appear in the config file")
	}
	secretPath := filepath.Join(filepath.Dir(path), "ldap-secret")
	secret, err := os.ReadFile(secretPath)
	if err != nil {
		t.Fatalf("read %s: %v", secretPath, err)
	}
	if strings.TrimSpace(string(secret)) != readSecrets(t).LdapBindPassword {
		t.Error("ldap-secret does not hold the deployment's LDAP reader password")
	}
}

// TestRadicaleChallengesAnonymousDAV is the safety assertion: an unauthenticated
// collection request must be refused, and refused with the realm the configurator
// set. A 200 or 207 here means the calendar of every user on the box is readable
// by anyone who can reach the port.
func TestRadicaleChallengesAnonymousDAV(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	res, _ := davRequest(t, "PROPFIND", "/admin/", "", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous PROPFIND = %d, want 401", res.StatusCode)
	}
	if challenge := res.Header.Get("WWW-Authenticate"); !strings.Contains(challenge, "Basic") ||
		!strings.Contains(challenge, "Bloud") {
		t.Errorf("WWW-Authenticate = %q, want Basic with the Bloud realm", challenge)
	}
}

// TestRadicaleLDAPLogin is the behavioral core: a Bloud account, verified
// against the Authentik LDAP outpost, gets its own DAV tree. This is the chain
// the app exists to provide, and nothing short of a 207 demonstrates it.
func TestRadicaleLDAPLogin(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	password := readSecrets(t).AuthentikBootstrapPassword
	if password == "" {
		t.Fatal("authentikBootstrapPassword not found in secrets.json")
	}

	res, body := davRequest(t, "PROPFIND", "/admin/", "admin", password)
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND /admin/ as admin = %d, want 207\nbody: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "multistatus") {
		t.Errorf("response is not a DAV multistatus document:\n%s", body)
	}
}

// TestRadicaleRejectsAWrongPassword keeps the passing case honest: the login
// above must pass because the credential matched, not because the server
// answers 207 to anything.
func TestRadicaleRejectsAWrongPassword(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	res, _ := davRequest(t, "PROPFIND", "/admin/", "admin", "not-the-password")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-password PROPFIND = %d, want 401", res.StatusCode)
	}
}

// TestRadicaleIsolatesOneUsersTreeFromAnother is the rights assertion. It uses
// the LDAP service account as the second identity because its credential is
// already in secrets.json; the account is a real directory user, which is all
// the test needs.
func TestRadicaleIsolatesOneUsersTreeFromAnother(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	res, _ := davRequest(t, "PROPFIND", "/admin/", "ldap-service", readSecrets(t).LdapBindPassword)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("ldap-service reading /admin/ = %d, want 403 (owner_only must not admit a stranger)",
			res.StatusCode)
	}
}

// TestRadicaleWritesACalendar proves the storage tree is actually writable by
// the container user. Under rootless Podman the app writes as a subordinate
// uid the host agent cannot become, so a directory the configurator failed to
// widen shows up here and nowhere else.
func TestRadicaleWritesACalendar(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	password := readSecrets(t).AuthentikBootstrapPassword
	calendarPath := fmt.Sprintf("/admin/bloud-e2e-%d/", time.Now().UnixNano())

	res, body := davRequest(t, "MKCALENDAR", calendarPath, "admin", password)
	if res.StatusCode != http.StatusCreated && res.StatusCode != http.StatusNoContent {
		t.Fatalf("MKCALENDAR %s = %d, want 201\nbody: %s", calendarPath, res.StatusCode, body)
	}

	// The collection must show up in the parent listing, which is what a
	// calendar client does next. Depth 1 is what asks for the children: a
	// header-less PROPFIND answers with /admin/ alone and never lists them.
	res, listing := davRequestDepth(t, "PROPFIND", "/admin/", "admin", password, "1")
	if res.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND /admin/ after create = %d, want 207", res.StatusCode)
	}
	if !strings.Contains(listing, strings.Trim(calendarPath, "/")) {
		t.Errorf("created calendar is absent from the parent listing:\n%s", listing)
	}
}

// TestRadicaleServesWellKnownDiscovery is what makes the app usable from a
// calendar client that is given only a bare hostname. Both paths have to
// redirect to the DAV root rather than 404, and the redirect must stay on
// the app origin: a discovery redirect that points somewhere the client
// cannot reach is the difference between "add my calendar" and a timeout.
func TestRadicaleServesWellKnownDiscovery(t *testing.T) {
	waitAppRunning(t, "radicale", 3*time.Minute)

	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	parsed, err := url.Parse(radicaleURL)
	if err != nil {
		t.Fatalf("parse %s: %v", radicaleURL, err)
	}
	for _, kind := range []string{"caldav", "carddav"} {
		path := "/.well-known/" + kind
		res, err := client.Get(radicaleURL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = res.Body.Close()
		if res.StatusCode < 300 || res.StatusCode >= 400 {
			t.Errorf("GET %s = %d, want a 3xx redirect to the DAV root", path, res.StatusCode)
			continue
		}
		loc := res.Header.Get("Location")
		if loc == "" {
			t.Errorf("GET %s: no Location header (status %d)", path, res.StatusCode)
			continue
		}
		target, err := url.Parse(loc)
		if err != nil {
			t.Errorf("Location %q for %s is not a URL: %v", loc, kind, err)
			continue
		}
		if target.Host != "" && target.Host != parsed.Host {
			t.Errorf("GET %s redirects to host %q, want the app host %q",
				path, target.Host, parsed.Host)
		}
	}
}
