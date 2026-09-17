// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/sharing"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/traefikgen"
)

// Contract (docs/operations/tech-debt.md, "route-generation side
// effects"): RegenerateRoutes is pure with respect to the runtime — it
// starts nothing and mutates no proxies; all runtime-shaped inputs are
// passed in. SyncRoutes owns the explicit ordering: gateway up, proxies
// reconciled, domain resolved, THEN config written.

// orderTracker records cross-fake call order. The SyncRoutes/
// RegenerateRoutes paths are synchronous single-goroutine by contract,
// so no locking is needed.
type orderTracker struct {
	calls          []string
	capturedRoutes []traefikgen.RemoteAppRoute
	capturedDomain string
}

func (tr *orderTracker) add(name string) { tr.calls = append(tr.calls, name) }

type orderGateway struct {
	tr  *orderTracker
	dom string
}

func (g *orderGateway) EnsureRunning(_ context.Context) error { g.tr.add("ensure-gateway"); return nil }
func (g *orderGateway) StopAndPurge(_ context.Context) error  { return nil }
func (g *orderGateway) GetTailnetDomain(_ context.Context) (string, error) {
	g.tr.add("tailnet-domain")
	return g.dom, nil
}

type orderProxy struct {
	tr      *orderTracker
	portMap map[string]int
}

func (p *orderProxy) StopAll() { p.tr.add("stop-all") }
func (p *orderProxy) Reconcile(_ []sharing.ProxyTarget) map[string]int {
	p.tr.add("reconcile-proxies")
	return p.portMap
}

type orderGenerator struct {
	tr *orderTracker
}

func (g *orderGenerator) Generate(_ []*catalog.App) error { return nil }
func (g *orderGenerator) SetAuthentikEnabled(_ bool)     {}
func (g *orderGenerator) Preview(_ []*catalog.App) string { return "" }
func (g *orderGenerator) GenerateAll(apps []*catalog.App, remoteApps []traefikgen.RemoteAppRoute, tailnetDomain string) error {
	g.tr.add("generate-all")
	g.tr.capturedRoutes = remoteApps
	g.tr.capturedDomain = tailnetDomain
	return nil
}

func newRouteSyncOrchestrator(tailnetID string) (*Orchestrator, *orderTracker) {
	tr := &orderTracker{}

	appStore := NewFakeAppStore()
	_ = appStore.Install("jellyfin", "Jellyfin", "1.0", nil, &store.InstallOptions{Port: 8096})
	catalogCache := NewFakeCatalogCache()
	catalogCache.AddApp(&catalog.App{CatalogID: "jellyfin", DisplayName: "Jellyfin", Version: "1.0.0", Port: 8096})

	remoteStore := NewFakeRemoteAppStore()
	_ = remoteStore.Create(store.RemoteApp{
		AppID: "jellyfin", HostLabel: "rem", TailnetAddr: "100.1.2.3:443",
	})

	orch := NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		catalogCache,
		"/tmp/bloud-test",
		newTestLogger(),
		OrchestratorConfig{
			AppStore:        appStore,
			RemoteAppStore:  remoteStore,
			Gateway:         &orderGateway{tr: tr, dom: "box.ts.net"},
			RemoteProxy:     &orderProxy{tr: tr, portMap: map[string]int{"jellyfin-rem": 5001}},
			TraefikGen:      &orderGenerator{tr: tr},
			ActiveTailnetID: func() string { return tailnetID },
		},
	)
	return orch, tr
}

// RegenerateRoutes must not touch the gateway or the proxy manager even
// though both are configured: runtime-shaped inputs come from the call
// arguments, nowhere else.
func TestRoutePurity_RegenerateRoutesTouchesNoRuntime(t *testing.T) {
	orch, tr := newRouteSyncOrchestrator("tn-1")

	routes := []traefikgen.RemoteAppRoute{{ID: "x", ProxyURL: "http://localhost:9"}}
	require.NoError(t, orch.RegenerateRoutes(routes, "d.ts.net"))

	assert.Equal(t, []string{"generate-all"}, tr.calls,
		"pure generator: only the config write happens")
	assert.Equal(t, routes, tr.capturedRoutes, "passed-in routes flow through untouched")
	assert.Equal(t, "d.ts.net", tr.capturedDomain)
}

// SyncRoutes orders the runtime steps before the config write and pipes
// their results into it.
func TestRoutePurity_SyncRoutesOrderAndPiping(t *testing.T) {
	orch, tr := newRouteSyncOrchestrator("tn-1")

	require.NoError(t, orch.SyncRoutes())

	assert.Equal(t,
		[]string{"ensure-gateway", "reconcile-proxies", "tailnet-domain", "generate-all"},
		tr.calls,
		"gateway up, proxies reconciled, domain resolved, then config written")
	require.Len(t, tr.capturedRoutes, 1)
	assert.Equal(t, "http://localhost:5001", tr.capturedRoutes[0].ProxyURL,
		"reconciled proxy port reaches the config")
	assert.Equal(t, "box.ts.net", tr.capturedDomain)
}

// No tailnet: the gateway is not started or queried, the domain is
// empty, and the config still writes — local apps need routes regardless.
func TestRoutePurity_InactiveTailnetSkipsGateway(t *testing.T) {
	orch, tr := newRouteSyncOrchestrator("")

	require.NoError(t, orch.SyncRoutes())

	assert.Equal(t, []string{"reconcile-proxies", "generate-all"}, tr.calls,
		"gateway untouched without a tailnet")
	assert.Empty(t, tr.capturedDomain)
}
