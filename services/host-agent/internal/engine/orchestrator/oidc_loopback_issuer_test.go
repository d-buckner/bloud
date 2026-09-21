// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package orchestrator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
)

// newIssuerTestOrchestrator wires an orchestrator with a real host state and a
// catalog holding two native-oidc apps: one on the loopback issuer, one on the
// shared issuer host.
func newIssuerTestOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()

	catCache := NewFakeCatalogCache()
	catCache.AddApp(&catalog.App{
		CatalogID: "hermes",
		SSO:       catalog.SSO{Strategy: "native-oidc", ClientType: "public", LoopbackIssuer: true},
	})
	catCache.AddApp(&catalog.App{
		CatalogID: "immich",
		SSO:       catalog.SSO{Strategy: "native-oidc"},
	})

	state := hostset.NewState(hostset.New(hostset.BuiltinHosts, hostset.DefaultPrimary))
	return NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		new(MockConfiguratorRegistry),
		catCache,
		t.TempDir(),
		newTestLogger(),
		OrchestratorConfig{Hosts: state, SSOHostSecret: "test-host-secret"},
	)
}

// TestOIDCInputsForApp_LoopbackIssuer covers the issuer a native-oidc app is
// handed: the shared issuer host (sso.localhost) by default, and the host
// loopback when the app's OIDC client accepts http only on a literal loopback
// hostname. The loopback issuer is what makes Hermes' self-hosted provider
// register instead of failing the dashboard closed at bind.
func TestOIDCInputsForApp_LoopbackIssuer(t *testing.T) {
	orch := newIssuerTestOrchestrator(t)
	urls := orch.resolveSSOURLs()

	loopbackApp, err := orch.catalog.Get("hermes")
	require.NoError(t, err)
	require.NotNil(t, loopbackApp)
	inputs := orch.oidcInputsForApp(loopbackApp, urls)
	require.NotNil(t, inputs)
	assert.Equal(t, "http://localhost:8080/application/o/hermes/", inputs.IssuerURL)
	// The client is still registered as a public PKCE client (no secret), and
	// its redirect URIs stay on the app's public origins, not the issuer.
	assert.Empty(t, inputs.ClientSecret)
	assert.Contains(t, inputs.RedirectURIs, "http://hermes.localhost:8080")

	sharedApp, err := orch.catalog.Get("immich")
	require.NoError(t, err)
	require.NotNil(t, sharedApp)
	shared := orch.oidcInputsForApp(sharedApp, urls)
	require.NotNil(t, shared)
	assert.Equal(t, "http://sso.localhost:8080/application/o/immich/", shared.IssuerURL)
}

// TestApplyIssuerExtraHost_LoopbackIssuer confirms the shared issuer hostname
// mapping is skipped for a loopback-issuer app: it runs with the host network
// namespace, where localhost already reaches Traefik.
func TestApplyIssuerExtraHost_LoopbackIssuer(t *testing.T) {
	orch := newIssuerTestOrchestrator(t)

	var loopbackSpec containerruntime.Spec
	orch.applyIssuerExtraHost(&loopbackSpec, "hermes")
	assert.NotContains(t, loopbackSpec.ExtraHosts, "sso.localhost:host-gateway")

	var sharedSpec containerruntime.Spec
	orch.applyIssuerExtraHost(&sharedSpec, "immich")
	assert.Contains(t, sharedSpec.ExtraHosts, "sso.localhost:host-gateway")
}
