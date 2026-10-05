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

// plannedNodes returns every graph node the orchestrator would create for an
// app: one per container definition, or the catalog ID when the app declares
// no containers.
//
// This is deliberately all of them rather than the app's "primary" node. The
// orchestrator creates a node per container, and an app can legitimately own
// more than one node that has a configurator (Radicale and its sync sidecar,
// for example) alongside nodes that never need one (a bundled postgres). A
// heuristic that looked at only the primary node could not tell those apart,
// and it read a second configured node as a stale registry entry.
func plannedNodes(catalogID string, app *catalog.App) []string {
	defs := app.ContainerDefs()
	if len(defs) == 0 {
		return []string{catalogID}
	}
	nodes := make([]string, 0, len(defs))
	for _, def := range defs {
		nodes = append(nodes, def.Name)
	}
	return nodes
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
// System apps are excluded. Authentik's configurator is registered under
// apps-authentik-server while its other containers are not configured at all,
// and that mismatch is a separate open item, not something this test should
// pass judgment on.
func TestEveryPlannedNodeHasAConfigurator(t *testing.T) {
	apps.RegisterAll()
	registry := configurator.NewRegistry(slog.New(slog.DiscardHandler), configurator.Deps{})

	all, err := catalog.NewLoader(repoAppsDir(t)).LoadAll()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	declared := map[string]bool{}
	for _, name := range apps.NodeNames() {
		declared[name] = true
	}

	planned := map[string]string{}
	for id, app := range all {
		if app.IsSystem {
			continue
		}
		nodes := plannedNodes(id, app)
		for _, node := range nodes {
			planned[node] = id
			// A container the registry claims to configure must actually have
			// a factory. Containers it says nothing about (a bundled database)
			// are not this test's business.
			if declared[node] && !registry.Has(node) {
				t.Errorf("app %q plans node %q which the registry declares, but no configurator factory is registered for it: the install would run unconfigured", id, node)
			}
		}
		// And the app must be reachable: at least one of its nodes has to be
		// one the registry knows, or nothing configures it however many
		// containers it ships.
		configured := false
		for _, node := range nodes {
			if declared[node] && registry.Has(node) {
				configured = true
				break
			}
		}
		if !configured {
			t.Errorf("app %q plans %v, none of which is a registered configurator node: the install would run unconfigured", id, nodes)
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

// TestRegistryNodeListMatchesCatalogExactly requires every node the user-app
// registry declares to be a node the catalog actually plans. A declared node
// nothing plans is a stale entry: it survives an app's removal or rename and
// keeps asserting a shape the catalog stopped having.
//
// The reverse direction is not a set equality on purpose. The catalog plans
// containers no configurator exists for (a bundled postgres needs none), so
// "planned" is a strict superset of "declared". The half that matters, that
// every app has at least one configured node, is asserted in
// TestEveryPlannedNodeHasAConfigurator.
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
		for _, node := range plannedNodes(id, app) {
			planned[node] = true
		}
	}

	for _, n := range apps.NodeNames() {
		if !planned[n] {
			t.Errorf("the user-app registry declares node %q, but no non-system app in the catalog plans it: a stale or mistyped entry in apps.NodeNames()", n)
		}
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
		configured := false
		for _, node := range plannedNodes(id, app) {
			if registry.Has(node) {
				configured = true
			}
		}
		if !configured {
			t.Errorf("app %q: none of its planned nodes %v resolves to a configurator", id, plannedNodes(id, app))
		}
		checked++
	}
	if checked < 5 {
		t.Fatalf("only %d multi-container apps checked; the catalog did not load as expected", checked)
	}
}
