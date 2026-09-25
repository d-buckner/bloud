// SPDX-License-Identifier: AGPL-3.0-only

package wire

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"codeberg.org/d-buckner/bloud/apps"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// repoAppsDir locates the catalog directory at the repo root by walking up
// from the test's working directory, so the test does not hardcode a depth
// that breaks if this package moves.
func repoAppsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "apps")
		if _, err := os.Stat(filepath.Join(candidate, "registry.go")); err == nil {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not locate the repo apps/ directory")
	return ""
}

// plannedNode returns the graph node the orchestrator would create for an
// app's primary service: the last container definition for a multi-container
// app, or the catalog ID when the app declares no containers. This mirrors
// the orchestrator's own primaryContainerNode convention.
func plannedNode(catalogID string, app *catalog.App) string {
	defs := app.ContainerDefs()
	if len(defs) == 0 {
		return catalogID
	}
	return defs[len(defs)-1].Name
}

// TestEveryPlannedNodeHasAConfigurator pins the agreement between the
// catalog and the configurator registry.
//
// The orchestrator resolves a configurator by graph node name, not by
// catalog ID. When those two disagree the app installs, the lookup returns
// nil, and nothing configures it: no error, no failed install, just an app
// that never got its SSO client or its config file. A CLI path deleted in
// this refactor failed exactly that way for every app in the catalog, and
// nothing noticed because nothing asserted the two lists agree.
//
// System apps are excluded. Authentik's last container is
// apps-authentik-ldap while its registered configurator node is
// apps-authentik-server, and that mismatch is a separate open item, not
// something this test should pass judgement on.
func TestEveryPlannedNodeHasAConfigurator(t *testing.T) {
	apps.RegisterAll()
	registry := configurator.NewRegistry(slog.New(slog.DiscardHandler), configurator.Deps{})

	all, err := catalog.NewLoader(repoAppsDir(t)).LoadAll()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	planned := map[string]string{}
	for id, app := range all {
		if app.IsSystem {
			continue
		}
		node := plannedNode(id, app)
		planned[node] = id
		if !registry.Has(node) {
			t.Errorf("app %q plans node %q, but no configurator factory is registered for it: the install would run unconfigured", id, node)
		}
	}

	if len(planned) < 10 {
		t.Fatalf("only %d non-system apps planned; the catalog did not load as expected, so this test would be near-vacuous", len(planned))
	}

	for _, name := range apps.NodeNames() {
		if _, ok := planned[name]; !ok {
			t.Errorf("the user-app registry declares node %q, but no non-system app in the catalog plans it: a stale or mistyped entry in apps.NodeNames()", name)
		}
	}
}

// TestRegistryNodeListMatchesCatalogExactly requires the two lists to be the
// same size in both directions, not merely to overlap.
func TestRegistryNodeListMatchesCatalogExactly(t *testing.T) {
	apps.RegisterAll()

	all, err := catalog.NewLoader(repoAppsDir(t)).LoadAll()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	planned := map[string]bool{}
	for id, app := range all {
		if app.IsSystem {
			continue
		}
		planned[plannedNode(id, app)] = true
	}

	declared := map[string]bool{}
	for _, n := range apps.NodeNames() {
		if declared[n] {
			t.Errorf("apps.NodeNames() lists %q twice", n)
		}
		declared[n] = true
	}

	if len(declared) != len(planned) {
		t.Errorf("registry declares %d nodes, the catalog plans %d", len(declared), len(planned))
	}
}

// TestCatalogIDIsNotANodeName records why the distinction this whole test
// file exists for is not pedantry. For a multi-container app the catalog ID
// resolves to nothing in the registry, so code that looks up configurators by
// catalog ID silently configures nothing while reporting success.
func TestCatalogIDIsNotANodeName(t *testing.T) {
	apps.RegisterAll()
	registry := configurator.NewRegistry(slog.New(slog.DiscardHandler), configurator.Deps{})

	all, err := catalog.NewLoader(repoAppsDir(t)).LoadAll()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	checked := 0
	for id, app := range all {
		if app.IsSystem || len(app.ContainerDefs()) == 0 {
			continue
		}
		if registry.Has(id) {
			t.Errorf("app %q: the catalog ID unexpectedly resolves to a configurator; the node-name distinction this test guards would be meaningless", id)
		}
		if !registry.Has(plannedNode(id, app)) {
			t.Errorf("app %q: the planned node %q should resolve", id, plannedNode(id, app))
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d multi-container apps checked; the catalog did not load as expected", checked)
	}
}
