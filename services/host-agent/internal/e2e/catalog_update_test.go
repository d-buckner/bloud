// SPDX-License-Identifier: AGPL-3.0-only

//go:build integration

// Package e2e's catalog-update tests drive the reconciler through a real
// catalog mutation against the live runtime: a configurator-less fixture app is
// written into the deployed catalog, installed through the API, then its
// metadata.yaml is rewritten and the catalog refreshed. The assertions are the
// behavioral outcomes the plan promises (a dropped container is pruned, a
// bumped image recreates exactly that container, a strategy flip deprovisions
// the old Authentik provider), verified against podman and the Authentik API.
package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fixtureApp = "e2e-catalog-fixture"

const (
	fixtureProxyProviderName  = "E2E Catalog Fixture Proxy Provider"
	fixtureOAuth2ProviderName = "E2E Catalog Fixture OAuth2 Provider"
)

// fixtureYAML renders one catalog entry for the fixture app. The five variants
// are generated rather than pasted because they are one concept with one thing
// changed each, and five hand-synced blobs make that change expensive enough to
// get wrong.
//
// Every container gets the shared {{appDataDir}}/data mount, and that is the
// reason to generate. The orchestrator creates apps/<app> only as a side
// effect of rendering a directory mount, so a fixture with no volumes has no
// data tree at all, and a "the data survived the prune" check against a tree
// that was never there cannot fail. Mounting on every container, not just the
// one a given test drops, is what keeps that check armed no matter which
// container a later variant removes.
//
// The mount also has to be uniform across any pair of variants diffed against
// each other. A mount present in fixtureV1 and absent in fixtureV2 is a spec
// change, and the tests asserting that only the intended container was
// recreated would then fail on the mount instead of on the thing under test.
//
// containers are container-name suffixes; "suffix@image" overrides the default
// image for that container.
func fixtureYAML(version, strategy string, containers ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "name: %s\ndisplayName: E2E Catalog Fixture\n", fixtureApp)
	b.WriteString("description: Integration fixture for catalog-update reconciliation\n")
	b.WriteString("category: media\n")
	fmt.Fprintf(&b, "version: %s\nport: 9999\nsso:\n  strategy: %s\ncontainers:\n", version, strategy)
	for _, c := range containers {
		suffix, image := c, "docker.io/alpine:3.20"
		if at := strings.Index(c, "@"); at >= 0 {
			suffix, image = c[:at], c[at+1:]
		}
		fmt.Fprintf(&b, "  - name: apps-%s-%s\n", fixtureApp, suffix)
		fmt.Fprintf(&b, "    image: %s\n", image)
		b.WriteString("    command: [\"sleep\", \"infinity\"]\n")
		b.WriteString("    network: apps-net\n")
		b.WriteString("    restartPolicy: always\n")
		b.WriteString("    volumes:\n")
		b.WriteString("      - source: \"{{appDataDir}}/data\"\n")
		b.WriteString("        destination: /data\n")
	}
	return b.String()
}

// The variants. Each differs from fixtureV1 in exactly the one thing its name
// claims, which is the property the generator makes structural: dropping
// container c is one argument, not a blob edit that might also move a volume.
var (
	fixtureV1          = fixtureYAML("1.0.0", "none", "a", "b", "c")
	fixtureV2          = fixtureYAML("2.0.0", "none", "a", "b")
	fixtureImageBump   = fixtureYAML("2.0.0", "none", "a@docker.io/alpine:3.21", "b", "c")
	fixtureForwardAuth = fixtureYAML("2.0.0", "forward-auth", "a", "b")
	fixtureNativeOIDC  = fixtureYAML("2.0.0", "native-oidc", "a", "b")
)

func requireCatalogDir(t *testing.T) {
	t.Helper()
	if os.Getenv("BLOUD_APPS_DIR") == "" {
		t.Fatal("BLOUD_APPS_DIR is not set; the integration tier must expose the catalog directory")
	}
}

func fixtureDir() string {
	return filepath.Join(os.Getenv("BLOUD_APPS_DIR"), fixtureApp)
}

func writeFixture(t *testing.T, yaml string) {
	t.Helper()
	dir := fixtureDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir fixture dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatalf("write fixture metadata: %v", err)
	}
}

func refreshCatalog(t *testing.T) {
	t.Helper()
	postJSON(t, hostAgentURL+"/api/apps/refresh-catalog", "", http.StatusOK)
}

func installFixture(t *testing.T) {
	t.Helper()
	postJSON(t, hostAgentURL+"/api/apps/"+fixtureApp+"/install", `{}`, http.StatusAccepted)
}

// cleanupFixture uninstalls the fixture while its catalog entry still exists,
// then removes the fixture directory and refreshes so the catalog no longer
// advertises it. Uninstall first: once the directory is gone the multi-container
// removal path has no ContainerDefs to iterate.
func cleanupFixture(t *testing.T) {
	t.Helper()
	if appStatus(t, fixtureApp) != "" {
		postJSON(t, hostAgentURL+"/api/apps/"+fixtureApp+"/uninstall", `{"clearData":true}`, http.StatusAccepted)
		deadline := time.Now().Add(4 * time.Minute)
		for time.Now().Before(deadline) {
			if appStatus(t, fixtureApp) == "" {
				break
			}
			time.Sleep(3 * time.Second)
		}
	}
	_ = os.RemoveAll(fixtureDir())
	refreshCatalog(t)
}

func containerExists(name string) bool {
	_, err := exec.Command("podman", "container", "exists", name).CombinedOutput()
	return err == nil
}

func requireContainerExists(t *testing.T, name string) {
	t.Helper()
	if !containerExists(name) {
		t.Errorf("container %s should still exist", name)
	}
}

func waitContainerGone(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !containerExists(name) {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("container %s still exists after %s", name, timeout)
}

func containerImage(name string) string {
	out, err := exec.Command("podman", "inspect", "-f", "{{.Config.Image}}", name).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func waitContainerImage(t *testing.T, name, wantTag string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(containerImage(name), wantTag) {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("container %s never reached image tag %q (last %q)", name, wantTag, containerImage(name))
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// providerExists reports whether an Authentik provider with the given name
// exists, returning false on any error so it is safe to poll.
func providerExists(t *testing.T, providerType, name string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		authentikURL+"/api/v3/providers/"+providerType+"/?search="+url.QueryEscape(name), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+authentikToken(t))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var list struct {
		Results []authentikProviderInfo `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return false
	}
	for _, r := range list.Results {
		if r.Name == name {
			return true
		}
	}
	return false
}

// A catalog change that drops a container prunes exactly that container, leaves
// the survivors running, and keeps the app's data directory.
func TestCatalogUpdate_RemovedContainer(t *testing.T) {
	requireCatalogDir(t)
	ensureAppUninstalled(t, fixtureApp)
	writeFixture(t, fixtureV1)
	refreshCatalog(t)
	t.Cleanup(func() { cleanupFixture(t) })

	installFixture(t)
	waitAppRunning(t, fixtureApp, 3*time.Minute)
	requireContainerExists(t, "apps-e2e-catalog-fixture-c")

	// Seed the witness inside the shared mount source, not beside it. Every
	// fixture container mounts the same tree, so a prune that cleans up "its
	// own" mount destroys data the survivors are still using, and that is the
	// bug worth catching. A witness parked outside the mount survives it.
	//
	// The pre-prune stat is what keeps this honest about the precondition: the
	// orchestrator creates apps/<app> only as a side effect of rendering a
	// directory mount, so a fixture with no volumes has no tree to witness.
	sharedTree := filepath.Join(appDataDir(fixtureApp), "data")
	if _, err := os.Stat(sharedTree); err != nil {
		t.Fatalf("install should have created the shared data mount source: %v", err)
	}
	witness := filepath.Join(sharedTree, "prune-witness.txt")
	const witnessBody = "still here"
	if err := os.WriteFile(witness, []byte(witnessBody), 0644); err != nil {
		t.Fatalf("seeding app data: %v", err)
	}

	writeFixture(t, fixtureV2)
	refreshCatalog(t)

	waitContainerGone(t, "apps-e2e-catalog-fixture-c", 2*time.Minute)
	requireContainerExists(t, "apps-e2e-catalog-fixture-a")
	requireContainerExists(t, "apps-e2e-catalog-fixture-b")

	if got, err := os.ReadFile(witness); err != nil || string(got) != witnessBody {
		t.Errorf("app data must survive a container prune: got %q (err %v)", got, err)
	}
}

// A bumped image tag recreates only the changed container, not its siblings.
func TestCatalogUpdate_ImageBump(t *testing.T) {
	requireCatalogDir(t)
	ensureAppUninstalled(t, fixtureApp)
	writeFixture(t, fixtureV1)
	refreshCatalog(t)
	t.Cleanup(func() { cleanupFixture(t) })

	installFixture(t)
	waitAppRunning(t, fixtureApp, 3*time.Minute)

	writeFixture(t, fixtureImageBump)
	refreshCatalog(t)

	waitContainerImage(t, "apps-e2e-catalog-fixture-a", ":3.21", 3*time.Minute)
	if img := containerImage("apps-e2e-catalog-fixture-b"); !strings.Contains(img, ":3.20") {
		t.Errorf("unchanged sibling b was recreated: image %q", img)
	}
}

// A strategy flip from forward-auth to native-oidc deprovisions the old proxy
// provider and provisions the new OAuth2 provider, asserted against Authentik.
func TestCatalogUpdate_SSOStrategyChange(t *testing.T) {
	requireCatalogDir(t)
	ensureAppUninstalled(t, fixtureApp)
	writeFixture(t, fixtureForwardAuth)
	refreshCatalog(t)
	t.Cleanup(func() { cleanupFixture(t) })

	installFixture(t)
	waitAppRunning(t, fixtureApp, 3*time.Minute)
	waitFor(t, "the forward-auth proxy provider to exist", 2*time.Minute, func() bool {
		return providerExists(t, "proxy", fixtureProxyProviderName)
	})

	writeFixture(t, fixtureNativeOIDC)
	refreshCatalog(t)

	waitFor(t, "the old proxy provider to be deleted", 3*time.Minute, func() bool {
		return !providerExists(t, "proxy", fixtureProxyProviderName)
	})
	waitFor(t, "the new OAuth2 provider to exist", 2*time.Minute, func() bool {
		return providerExists(t, "oauth2", fixtureOAuth2ProviderName)
	})
}
