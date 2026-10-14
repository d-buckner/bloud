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

var podlyURL = getEnvDefault("BLOUD_E2E_PODLY_URL", "http://localhost:5001")

// The two accounts Bloud maintains inside the app, and the published secret key
// the human one's password lives under. They must match apps/podly: the app has
// no way to report what it was created with, so these names are the contract
// between the configurator and this test.
const (
	podlyServiceUser = "bloud-service"
	podlyHumanUser   = "bloud"
	podlySecretName  = "password"
)

func podlyPublicURL() string {
	return bloudSubdomainURL("podly")
}

// TestPodlyInstallViaAPI installs Podly through the API.
//
// The deadline is longer than any other app's, and it is still a fraction of the
// tier's 30-minute budget: the pinned image carries local Whisper and its CPU
// torch, 4.73 GB of compressed layers, and the pull happens before the container
// can answer the health check that promotes the node.
func TestPodlyInstallViaAPI(t *testing.T) {
	postJSON(t, hostAgentURL+"/api/apps/podly/install", `{}`, http.StatusAccepted)
	waitAppRunning(t, "podly", 12*time.Minute)
	waitHTTPOrFatal(t, 60*time.Second, podlyURL+"/")
	waitRoutedApp(t, podlyPublicURL()+"/", 2*time.Minute)
}

// TestPodlyServesWithItsOwnAuthEnabled is the first rung of the design. Bloud
// turns the app's authentication on rather than bypassing it at the ingress, so
// the running app must report auth on. If this is false the feeds are open to
// anyone who can count, because the feed ids are sequential integers.
func TestPodlyServesWithItsOwnAuthEnabled(t *testing.T) {
	waitAppRunning(t, "podly", 2*time.Minute)

	for _, base := range []string{podlyURL, podlyPublicURL()} {
		resp, err := http.Get(base + "/api/auth/status")
		if err != nil {
			t.Fatalf("GET %s/api/auth/status: %v", base, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s/api/auth/status: status %d, want 200: %s", base, resp.StatusCode, body)
		}
		var status struct {
			RequireAuth bool `json:"require_auth"`
		}
		if err := json.Unmarshal(body, &status); err != nil {
			t.Fatalf("decoding auth status from %s: %v", base, err)
		}
		if !status.RequireAuth {
			t.Fatalf("%s is serving with REQUIRE_AUTH off; every feed is public", base)
		}
	}
}

// TestPodlyEnvFileStaysOnTheHost checks the delivery channel. The env file is
// read on the host at container create and is deliberately not mounted, so it
// must exist with the values the configurator committed, with a mode that keeps
// the service password out of every other reader's reach, and with the same
// service password the host store holds (which is what lets a later pass sign in
// to reconcile accounts).
func TestPodlyEnvFileStaysOnTheHost(t *testing.T) {
	waitAppRunning(t, "podly", 2*time.Minute)

	path := filepath.Join(appDataDir("podly"), "config", "env")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("%s mode = %o, want 600 (it holds a password)", path, perm)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if values["REQUIRE_AUTH"] != "true" {
		t.Errorf("REQUIRE_AUTH = %q, want true", values["REQUIRE_AUTH"])
	}
	if values["PODLY_ADMIN_USERNAME"] != podlyServiceUser {
		t.Errorf("PODLY_ADMIN_USERNAME = %q, want %q", values["PODLY_ADMIN_USERNAME"], podlyServiceUser)
	}
	if values["PODLY_SECRET_KEY"] == "" {
		t.Error("PODLY_SECRET_KEY is empty; every restart would sign out every session")
	}
	if got, want := values["PODLY_ADMIN_PASSWORD"], readSecrets(t).AppSecrets["podly"].AdminPassword; got == "" || got != want {
		t.Error("the env file's service password is not the non-empty value the secret store holds")
	}
}

// TestPodlyClientCredentialLifecycle walks the one credential a person uses
// through every control Bloud offers for it, in the order the controls build on
// each other: reveal once, rotate, revoke.
//
// It is one test with subtests rather than four because the value is single-use
// by design. A second reveal is refused, so any test that wanted the value after
// the first reveal would have to rotate, and a rotation would silently change
// what the next test asserted about. Sharing one value in one scope is the only
// honest shape.
func TestPodlyClientCredentialLifecycle(t *testing.T) {
	waitAppRunning(t, "podly", 2*time.Minute)

	// The reveal is the only thing that hands the value over, so it comes first
	// and everything after it works from what it returned.
	password := podlyCredential(t, "reveal", http.StatusOK)
	if password == "" {
		t.Fatal("the reveal returned no value")
	}

	t.Run("the revealed password signs in", func(t *testing.T) {
		if status := podlyLogin(t, podlyHumanUser, password); status != http.StatusOK {
			t.Fatalf("signing in as %s with the revealed password: status %d, want 200",
				podlyHumanUser, status)
		}
		// The guard on the guard: if the app had stopped checking passwords this
		// would pass for the wrong reason.
		if status := podlyLogin(t, podlyHumanUser, password+"-wrong"); status != http.StatusUnauthorized {
			t.Errorf("signing in with a wrong password: status %d, want 401", status)
		}
	})

	t.Run("a second reveal is refused", func(t *testing.T) {
		// `reveal: once` is the policy apps/podly declares, and the refusal is
		// what makes the first reveal mean something.
		if status := podlyCredentialStatus(t, "reveal"); status != http.StatusConflict {
			t.Errorf("second reveal: status %d, want 409", status)
		}
	})

	t.Run("rotation reaches the app", func(t *testing.T) {
		// This is the claim an environment-only delivery could not keep: Podly
		// reads PODLY_ADMIN_PASSWORD only while its user table is empty, so a
		// rotated value can arrive only through the app's own user API.
		session := podlySession(t, podlyHumanUser, password)
		rotated := podlyCredential(t, "rotate", http.StatusOK)
		if rotated == password {
			t.Fatal("rotation returned the value it already had")
		}

		deadline := time.Now().Add(8 * time.Minute)
		for podlyLogin(t, podlyHumanUser, rotated) != http.StatusOK {
			if time.Now().After(deadline) {
				t.Fatal("the rotated password never became the one the app accepts")
			}
			time.Sleep(5 * time.Second)
		}
		if status := podlyLogin(t, podlyHumanUser, password); status != http.StatusUnauthorized {
			t.Errorf("the pre-rotation password still authenticates (status %d); the rotation did not land", status)
		}
		// Rotation revokes sessions in the same intent pair, and the revoke is
		// what forces the recreate that installs the new signing key.
		if err := waitForSessionDeath(t, session); err != nil {
			t.Errorf("the pre-rotation session survived the rotation: %v", err)
		}
		password = rotated
	})

	t.Run("revoke ends every session without changing the password", func(t *testing.T) {
		session := podlySession(t, podlyHumanUser, password)
		if !podlySessionAlive(t, session) {
			t.Fatal("a fresh sign-in does not reach an authenticated endpoint")
		}

		postJSON(t, podlyCredentialURL("revoke"), `{}`, http.StatusAccepted)
		if err := waitForSessionDeath(t, session); err != nil {
			t.Error(err)
		}
		if status := podlyLogin(t, podlyHumanUser, password); status != http.StatusOK {
			t.Errorf("after a revoke the password should still sign in: status %d, want 200", status)
		}
	})
}

// --- helpers ---

func podlyCredentialURL(action string) string {
	return fmt.Sprintf("%s/api/apps/podly/client-credentials/%s/%s", hostAgentURL, podlySecretName, action)
}

// podlyCredential calls one of the credential controls and returns the value it
// handed over. Reveal and rotate are the only two that ever return a value.
func podlyCredential(t *testing.T, action string, want int) string {
	t.Helper()
	resp := agentPost(t, podlyCredentialURL(action), "application/json", strings.NewReader(`{}`))
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("POST %s: status %d (want %d): %s", action, resp.StatusCode, want, body)
	}
	var out struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decoding %s response: %v", action, err)
	}
	return out.Value
}

func podlyCredentialStatus(t *testing.T, action string) int {
	t.Helper()
	resp := agentPost(t, podlyCredentialURL(action), "application/json", strings.NewReader(`{}`))
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// podlyLogin signs in and returns the status only.
func podlyLogin(t *testing.T, username, password string) int {
	t.Helper()
	resp := podlyLoginRequest(t, username, password)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// podlySession signs in and returns the cookie a live session rides on.
func podlySession(t *testing.T, username, password string) *http.Cookie {
	t.Helper()
	resp := podlyLoginRequest(t, username, password)
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signing in as %s before the assertion: status %d", username, resp.StatusCode)
	}
	// The app names its own cookie (podly_session by default); taking the first
	// one keeps this from hard-coding a name the app lets an operator change.
	for _, c := range resp.Cookies() {
		if c.Name != "" {
			return c
		}
	}
	t.Fatal("the app issued no session cookie")
	return nil
}

func podlyLoginRequest(t *testing.T, username, password string) *http.Response {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":%q}`, username, password)
	resp, err := http.Post(podlyURL+"/api/auth/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /api/auth/login: %v", err)
	}
	return resp
}

// podlySessionAlive asks an authenticated endpoint whether a cookie still gets
// in. /api/auth/me answers 401 without a valid session and 200 with one.
func podlySessionAlive(t *testing.T, session *http.Cookie) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, podlyURL+"/api/auth/me", nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.AddCookie(session)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

// waitForSessionDeath waits for a revoke to land. The endpoint answers 202: the
// revocation is an intent, and it takes a convergence pass plus the recreate that
// installs the new signing key before an old cookie stops verifying.
func waitForSessionDeath(t *testing.T, session *http.Cookie) error {
	t.Helper()
	deadline := time.Now().Add(8 * time.Minute)
	for {
		if !podlySessionAlive(t, session) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the session was still valid after 8m; the signing key never changed")
		}
		time.Sleep(5 * time.Second)
	}
}
