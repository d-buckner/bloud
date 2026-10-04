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

const fixtureV1 = `name: e2e-catalog-fixture
displayName: E2E Catalog Fixture
description: Integration fixture for catalog-update reconciliation
category: media
version: 1.0.0
port: 9999
sso:
  strategy: none
containers:
  - name: apps-e2e-catalog-fixture-a
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-b
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-c
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
`

// fixtureV2 drops container c.
const fixtureV2 = `name: e2e-catalog-fixture
displayName: E2E Catalog Fixture
description: Integration fixture for catalog-update reconciliation
category: media
version: 2.0.0
port: 9999
sso:
  strategy: none
containers:
  - name: apps-e2e-catalog-fixture-a
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-b
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
`

// fixtureImageBump bumps container a's image only.
const fixtureImageBump = `name: e2e-catalog-fixture
displayName: E2E Catalog Fixture
description: Integration fixture for catalog-update reconciliation
category: media
version: 2.0.0
port: 9999
sso:
  strategy: none
containers:
  - name: apps-e2e-catalog-fixture-a
    image: docker.io/alpine:3.21
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-b
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-c
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
`

// fixtureForwardAuth is fixtureV1 under a forward-auth strategy.
const fixtureForwardAuth = `name: e2e-catalog-fixture
displayName: E2E Catalog Fixture
description: Integration fixture for catalog-update reconciliation
category: media
version: 2.0.0
port: 9999
sso:
  strategy: forward-auth
containers:
  - name: apps-e2e-catalog-fixture-a
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-b
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
`

// fixtureNativeOIDC is fixtureForwardAuth flipped to native-oidc.
const fixtureNativeOIDC = `name: e2e-catalog-fixture
displayName: E2E Catalog Fixture
description: Integration fixture for catalog-update reconciliation
category: media
version: 2.0.0
port: 9999
sso:
  strategy: native-oidc
containers:
  - name: apps-e2e-catalog-fixture-a
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
  - name: apps-e2e-catalog-fixture-b
    image: docker.io/alpine:3.20
    command: ["sleep", "infinity"]
    network: apps-net
    restartPolicy: always
`

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

	writeFixture(t, fixtureV2)
	refreshCatalog(t)

	waitContainerGone(t, "apps-e2e-catalog-fixture-c", 2*time.Minute)
	requireContainerExists(t, "apps-e2e-catalog-fixture-a")
	requireContainerExists(t, "apps-e2e-catalog-fixture-b")

	if _, err := os.Stat(appDataDir(fixtureApp)); err != nil {
		t.Errorf("app data dir must survive a container prune: %v", err)
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
