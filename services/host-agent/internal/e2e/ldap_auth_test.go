// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// waitLDAPReady blocks until the Authentik LDAP outpost accepts the service
// account bind, then returns. It fails the test with the last attempt if the
// outpost never answers.
//
// The outpost is a system container the orchestrator restarts whenever
// Authentik is re-provisioned, which every app install triggers. A test that
// runs after an install can therefore reach the outpost while it is still
// coming back, and an unreachable outpost is not a clean error: it surfaces as
// a 500 from an app that authenticates over LDAP (Radicale) or a 401 from
// Jellyfin, which reads like a broken app rather than a readiness gap. Waiting
// on a real bind turns that window into an explicit, named precondition.
//
// A TCP dial is not enough. The container publishes its port before it is
// ready to search, so the probe has to be a search that returns the base DN.
func waitLDAPReady(t *testing.T, timeout time.Duration) {
	t.Helper()
	password := readSecrets(t).LdapBindPassword
	if password == "" {
		t.Fatal("ldapBindPassword not found in secrets.json")
	}

	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.Command("ldapsearch",
			"-x",
			"-H", ldapURL,
			"-D", expectedLDAPBindUser,
			"-w", password,
			"-b", expectedLDAPBaseDN,
			"-s", "base",
			"(objectClass=*)",
		).CombinedOutput()
		if err == nil && strings.Contains(string(out), expectedLDAPBaseDN) {
			return
		}
		last = fmt.Sprintf("%v: %s", err, strings.TrimSpace(string(out)))
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("LDAP outpost at %s never accepted a bind within %s: %s", ldapURL, timeout, last)
}

// TestLDAPAuth_ServiceAccountCanBind is the key behavioral test for the LDAP
// token flow: the outpost accepts connections and the service account binds.
// In the product path the outpost container gets its real token via the
// shared template-var map during graph reconciliation; no container restart
// or env rewriting.
func TestLDAPAuth_ServiceAccountCanBind(t *testing.T) {
	waitLDAPReady(t, 3*time.Minute)
	ldapBindPassword := readSecrets(t).LdapBindPassword
	if ldapBindPassword == "" {
		t.Fatal("ldapBindPassword not found in secrets.json")
	}

	out := runCmd(t, "ldapsearch",
		"-x",
		"-H", ldapURL,
		"-D", expectedLDAPBindUser,
		"-w", ldapBindPassword,
		"-b", expectedLDAPBaseDN,
		"-s", "base",
		"(objectClass=*)",
	)
	if !strings.Contains(out, expectedLDAPBaseDN) {
		t.Errorf("LDAP search result does not contain base DN %q:\n%s", expectedLDAPBaseDN, out)
	}
	t.Log("LDAP service account bind successful")
}

// TestLDAPAuth_AuthentikAdminCanBind verifies the LDAP outpost serves real
// user data by binding as a real Authentik user (the admin account the
// configurator creates), not just the service account.
func TestLDAPAuth_AuthentikAdminCanBind(t *testing.T) {
	waitLDAPReady(t, 3*time.Minute)
	adminPassword := readSecrets(t).AuthentikBootstrapPassword
	if adminPassword == "" {
		t.Fatal("authentikBootstrapPassword not found in secrets.json")
	}

	out := runCmd(t, "ldapsearch",
		"-x",
		"-H", ldapURL,
		"-D", "cn=admin,ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-w", adminPassword,
		"-b", "ou=users,dc=ldap,dc=goauthentik,dc=io",
		"-s", "one",
		"(cn=admin)",
		"cn",
	)
	if !strings.Contains(out, "cn=admin") {
		t.Errorf("LDAP search did not return admin user:\n%s", out)
	}
	t.Log("admin LDAP authentication successful")
}

// --- Live state streaming (install 202 + SSE) ---
