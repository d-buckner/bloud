// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

package e2e

import (
	"strings"
	"testing"
)

// TestLDAPAuth_ServiceAccountCanBind is the key behavioral test for the LDAP
// token flow: the outpost accepts connections and the service account binds.
// In the product path the outpost container gets its real token via the
// shared template-var map during graph reconciliation; no container restart
// or env rewriting.
func TestLDAPAuth_ServiceAccountCanBind(t *testing.T) {
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
// user data by binding as the authentik admin user.

// TestLDAPAuth_AuthentikAdminCanBind verifies the LDAP outpost serves real
// user data by binding as the authentik admin user.
func TestLDAPAuth_AuthentikAdminCanBind(t *testing.T) {
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
