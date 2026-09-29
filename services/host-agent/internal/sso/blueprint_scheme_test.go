// SPDX-License-Identifier: AGPL-3.0-only

package sso

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Part B of the proxied-scheme plan (docs/plans/proxied-scheme-urls.md).
//
// Part A pinned what HostSet derives. This pins what actually reaches Authentik,
// because a correct derivation can still be rendered into the wrong artifact, and
// the artifact is what decides whether a login completes.
//
// Each SSO strategy fails differently behind a TLS terminator, so each gets its
// own assertions rather than one shared check:
//
//	native-oidc  the issuer and the registered redirect URIs are http, so the
//	             browser blocks mixed-content discovery or the redirect_uri
//	             does not match
//	forward-auth external_host is http, so the outpost redirects the browser
//	             off TLS on the very first unauthenticated request
//	ldap         the launch URL and the app-side redirect are http
//
// These decode the generated YAML rather than substring-matching it, following
// the internal/appconfig/traefik_test.go pattern: a substring assertion passes
// when the value moves to a different field or the template gains a second
// occurrence, and a decoded assertion does not.

const (
	testPublicHost  = "bloud.example.com"
	testPublicHTTP  = "http://" + testPublicHost
	testPublicHTTPS = "https://" + testPublicHost
)

// httpsGenerator builds a generator for a deployment behind a TLS-terminating
// proxy: every browser-facing URL is https, and the container-internal Authentik
// address stays plain HTTP because that hop never leaves the podman network.
func httpsGenerator(t *testing.T, dir string) *BlueprintGenerator {
	t.Helper()
	return NewBlueprintGenerator(
		"test-secret",
		"test-ldap-password",
		[]string{testPublicHTTPS},
		testPublicHTTPS,
		testPublicHTTPS,
		dir,
		nil,
	)
}

// decodeBlueprint parses a generated blueprint into a generic node tree. The
// templates carry Authentik's custom tags (!Find and !KeyOf), which a typed
// decode rejects, so the tests walk nodes and pull fields by name instead.
func decodeBlueprint(t *testing.T, content string) *yaml.Node {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(content), &node); err != nil {
		t.Fatalf("generated blueprint is not valid YAML: %v\n%s", err, content)
	}
	return &node
}

// scalarsUnder returns every scalar string in a subtree. For a mapping it walks
// values only, so a key's name is never mistaken for its value.
func scalarsUnder(n *yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		return []string{n.Value}
	case yaml.MappingNode:
		var out []string
		for i := 1; i < len(n.Content); i += 2 {
			out = append(out, scalarsUnder(n.Content[i])...)
		}
		return out
	default:
		var out []string
		for _, c := range n.Content {
			out = append(out, scalarsUnder(c)...)
		}
		return out
	}
}

// valuesForKey returns every scalar sitting under any mapping entry named key, at
// any depth in the tree.
func valuesForKey(n *yaml.Node, key string) []string {
	var out []string
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == key {
				out = append(out, scalarsUnder(n.Content[i+1])...)
			}
			out = append(out, valuesForKey(n.Content[i+1], key)...)
		}
	default:
		for _, c := range n.Content {
			out = append(out, valuesForKey(c, key)...)
		}
	}
	return out
}

// assertAllHTTPS fails with the offending values listed, because "some URL was
// http" is not actionable and "these three URLs were http" is.
func assertAllHTTPS(t *testing.T, field string, got []string) {
	t.Helper()
	if len(got) == 0 {
		t.Errorf("%s: expected at least one value, found none", field)
		return
	}
	var bad []string
	for _, u := range got {
		if strings.HasPrefix(u, "http://") {
			bad = append(bad, u)
		}
	}
	if len(bad) > 0 {
		t.Errorf("%s: browser-facing field carries plain http under a https deployment: %v", field, bad)
	}
}

func oidcApp() *catalog.App {
	return &catalog.App{
		CatalogID:   "immich",
		DisplayName: "Immich",
		Port:        2283,
		SSO: catalog.SSO{
			Strategy:     "native-oidc",
			CallbackPath: "/api/auth/openid/callback",
		},
	}
}

func TestOIDCBlueprint_CarriesPublicScheme(t *testing.T) {
	dir := t.TempDir()
	gen := httpsGenerator(t, dir)

	if err := gen.GenerateForApp(oidcApp()); err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "immich.yaml"))
	root := decodeBlueprint(t, content)

	redirects := valuesForKey(root, "url")
	assertAllHTTPS(t, "redirect_uris", redirects)

	wantRedirect := "https://immich." + testPublicHost + "/api/auth/openid/callback"
	if !contains(redirects, wantRedirect) {
		t.Errorf("redirect_uris = %v, want it to include %q", redirects, wantRedirect)
	}

	launches := valuesForKey(root, "meta_launch_url")
	assertAllHTTPS(t, "meta_launch_url", launches)
	if !contains(launches, "https://immich."+testPublicHost) {
		t.Errorf("meta_launch_url = %v, want https://immich.%s", launches, testPublicHost)
	}

	// The direct-port debug redirect is browser-facing too. It inherits the
	// scheme from the primary base URL, so a plain-http here means the debug
	// URI would never match under a https deployment.
	var portRedirects []string
	for _, u := range redirects {
		if strings.Contains(u, ":2283") {
			portRedirects = append(portRedirects, u)
		}
	}
	assertAllHTTPS(t, "direct-port redirect_uris", portRedirects)
}

func TestForwardAuthBlueprint_CarriesPublicScheme(t *testing.T) {
	dir := t.TempDir()
	gen := httpsGenerator(t, dir)

	app := &catalog.App{
		CatalogID:   "adguard-home",
		DisplayName: "AdGuard Home",
		Port:        3080,
		SSO:         catalog.SSO{Strategy: "forward-auth"},
	}

	if err := gen.GenerateForApp(app); err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "adguard-home.yaml"))
	root := decodeBlueprint(t, content)

	// external_host is the URL the outpost sends the browser to. If it is
	// http, the first unauthenticated request leaves TLS and the proxy has to
	// bounce it back, which is where the forward-auth flow stalls.
	external := valuesForKey(root, "external_host")
	assertAllHTTPS(t, "external_host", external)
	if !contains(external, testPublicHTTPS) {
		t.Errorf("external_host = %v, want %q", external, testPublicHTTPS)
	}

	assertAllHTTPS(t, "meta_launch_url", valuesForKey(root, "meta_launch_url"))
}

func TestOutpostBlueprint_BrowserURLCarriesPublicScheme(t *testing.T) {
	dir := t.TempDir()
	gen := httpsGenerator(t, dir)

	if err := gen.GenerateOutpostBlueprint([]ForwardAuthProvider{{DisplayName: "AdGuard Home"}}); err != nil {
		t.Fatalf("GenerateOutpostBlueprint failed: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "bloud-outpost.yaml"))
	root := decodeBlueprint(t, content)

	// authentik_host_browser is the URL the outpost redirects the browser to.
	// This is the single field that decides whether a forward-auth login stays
	// on TLS.
	browser := valuesForKey(root, "authentik_host_browser")
	assertAllHTTPS(t, "authentik_host_browser", browser)
	if !contains(browser, testPublicHTTPS) {
		t.Errorf("authentik_host_browser = %v, want %q", browser, testPublicHTTPS)
	}
}

func TestLDAPBlueprint_LaunchURLCarriesPublicScheme(t *testing.T) {
	dir := t.TempDir()
	gen := httpsGenerator(t, dir)

	app := &catalog.App{
		CatalogID:   "jellyfin",
		DisplayName: "Jellyfin",
		Port:        8096,
		SSO:         catalog.SSO{Strategy: "ldap"},
	}

	if err := gen.GenerateForApp(app); err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "jellyfin.yaml"))
	root := decodeBlueprint(t, content)

	assertAllHTTPS(t, "meta_launch_url", valuesForKey(root, "meta_launch_url"))
}

// The http baseline. Without a proxy the same templates must still emit http,
// so a change that forces https unconditionally fails here rather than quietly
// breaking every plain-HTTP deployment.
func TestBlueprints_KeepHTTPWhenThereIsNoProxy(t *testing.T) {
	dir := t.TempDir()
	gen := testBlueprintGenerator(t, dir)

	if err := gen.GenerateForApp(oidcApp()); err != nil {
		t.Fatalf("GenerateForApp failed: %v", err)
	}
	root := decodeBlueprint(t, readFile(t, filepath.Join(dir, "immich.yaml")))

	redirects := valuesForKey(root, "url")
	if len(redirects) == 0 {
		t.Fatal("no redirect_uris generated")
	}
	for _, u := range redirects {
		if !strings.HasPrefix(u, "http://") {
			t.Errorf("redirect_uri %q should stay plain http with no proxy configured", u)
		}
	}
	for _, u := range valuesForKey(root, "meta_launch_url") {
		if !strings.HasPrefix(u, "http://") {
			t.Errorf("meta_launch_url %q should stay plain http with no proxy configured", u)
		}
	}
}

// OIDCInputsForApp is what the orchestrator hands the provider, so its issuer
// must agree with what the browser is told. A mismatch here is the Layer 3
// contradiction showing up one level before the blueprint.
//
// Asserted on the parsed scheme rather than a string prefix: apps live on
// subdomains, so "https://immich.bloud.example.com" is correct even though it
// does not start with "https://bloud.example.com".
func TestOIDCInputs_IssuerMatchesPublicScheme(t *testing.T) {
	gen := httpsGenerator(t, t.TempDir())

	inputs := gen.OIDCInputsForApp(oidcApp())
	if inputs == nil {
		t.Fatal("OIDCInputsForApp returned nil for a native-oidc app")
	}
	if got := schemeOf(t, inputs.IssuerURL); got != "https" {
		t.Errorf("IssuerURL %q has scheme %q, want https", inputs.IssuerURL, got)
	}
	for _, u := range inputs.RedirectURIs {
		if got := schemeOf(t, u); got != "https" {
			t.Errorf("RedirectURI %q has scheme %q, want https", u, got)
		}
	}
	if got := schemeOf(t, inputs.LaunchURL); got != "https" {
		t.Errorf("LaunchURL %q has scheme %q, want https", inputs.LaunchURL, got)
	}
}

func schemeOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("%q is not a parseable URL: %v", raw, err)
	}
	return u.Scheme
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) failed: %v", path, err)
	}
	return string(b)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
