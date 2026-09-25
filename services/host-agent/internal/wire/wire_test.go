// SPDX-License-Identifier: AGPL-3.0-only

package wire

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	containerruntime "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/eventbus"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/hostset"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/secrets"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingRuntime is a container runtime that records every call. Build must
// not touch it: constructing an orchestrator is not the same as driving one.
type recordingRuntime struct {
	calls []string
}

func (r *recordingRuntime) EnsureNetwork(_ context.Context, name string) error {
	r.calls = append(r.calls, "EnsureNetwork:"+name)
	return nil
}

func (r *recordingRuntime) Ensure(_ context.Context, _ containerruntime.Spec) (containerruntime.EnsureResult, error) {
	r.calls = append(r.calls, "Ensure")
	return containerruntime.EnsureResult{}, nil
}

func (r *recordingRuntime) Remove(_ context.Context, name string) error {
	r.calls = append(r.calls, "Remove:"+name)
	return nil
}

func (r *recordingRuntime) Inspect(_ context.Context, name string) (containerruntime.State, error) {
	r.calls = append(r.calls, "Inspect:"+name)
	return containerruntime.State{}, nil
}

func (r *recordingRuntime) Exec(_ context.Context, name string, _ []string) error {
	r.calls = append(r.calls, "Exec:"+name)
	return nil
}

// baseInput returns a fully populated Input: every required field set, and
// every optional collaborator present so a test that removes one is asserting
// a deliberate difference rather than an accident.
func baseInput(t *testing.T) Input {
	t.Helper()

	db := testdb.SetupTestDB(t)
	appsDir := t.TempDir()

	return Input{
		Logger:            slog.New(slog.DiscardHandler),
		DB:                db,
		AppStore:          store.NewAppStore(db),
		CatalogCache:      catalog.NewMemoryCache(),
		Registry:          configurator.NewRegistry(slog.New(slog.DiscardHandler), configurator.Deps{}),
		ContainerRuntime:  &recordingRuntime{},
		EventsBus:         eventbus.New(),
		Authentik:         authentik.NewClient("http://127.0.0.1:9999", "test-token"),
		TailnetStore:      store.NewTailnetStore(db),
		HostStore:         store.NewHostStore(db),
		Hosts:             hostset.NewState(hostset.New(hostset.BuiltinHosts, hostset.DefaultPrimary)),
		AppsDir:           appsDir,
		DataDir:           t.TempDir(),
		TraefikDynamicDir: t.TempDir(),
		TraefikPort:       80,
		LDAPOutput:        &configurator.LDAPOutput{Host: "127.0.0.1", Port: 3389},
		TemplateVars:      map[string]string{"postgresPassword": "pw"},
		Secrets:           secrets.NewManager(filepath.Join(t.TempDir(), "secrets.json")),
		SSOBaseURL:        "http://localhost:8080",
		SSOHostSecret:     "host-secret",
		SSOAuthentikURL:   "http://sso.localhost:8080",
		SSOIssuerURL:      "http://sso.localhost:8080",
		OnHostsChanged:    func() {},
	}
}

func TestBuildReturnsAWiredOrchestrator(t *testing.T) {
	out, err := Build(baseInput(t))

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Orchestrator, "Build must return an orchestrator")
	assert.NotNil(t, out.Gateway, "the gateway is part of the wiring the builder owns")
	assert.NotNil(t, out.TailnetNode, "the tailnet node is part of the wiring the builder owns")
}

// Build must not drive anything. A builder that converged on its way out would
// start containers before the caller is ready, and would make the
// completeness assertions below race a live loop.
func TestBuildHasNoRuntimeSideEffects(t *testing.T) {
	rt := &recordingRuntime{}
	in := baseInput(t)
	in.ContainerRuntime = rt

	out, err := Build(in)
	require.NoError(t, err)

	assert.Empty(t, rt.calls, "Build must not call the container runtime")
	assert.True(t, out.Orchestrator.LastConverged().IsZero(),
		"Build must not run a convergence pass")
}

// The identity provider is optional: a runtime with no Authentik still boots,
// it just cannot provision SSO. Both SSO-facing config fields must go nil
// together, or a half-disabled provisioner is worse than an absent one.
func TestBuildWithoutAuthentikDisablesBothSSOProvisioners(t *testing.T) {
	in := baseInput(t)
	in.Authentik = nil

	out, err := Build(in)
	require.NoError(t, err)

	assert.Nil(t, out.Config.SSO, "no identity provider means no SSO provisioner")
	assert.Nil(t, out.Config.ForwardDomainSSO,
		"no identity provider means no forward-domain provisioner")
}

func TestBuildWithAuthentikWiresBothSSOProvisioners(t *testing.T) {
	out, err := Build(baseInput(t))
	require.NoError(t, err)

	assert.NotNil(t, out.Config.SSO)
	assert.NotNil(t, out.Config.ForwardDomainSSO)
}

// The supplied runtime wins over the podman fallback, so a caller that owns
// its runtime keeps ownership.
func TestBuildUsesTheSuppliedRuntime(t *testing.T) {
	rt := &recordingRuntime{}
	in := baseInput(t)
	in.ContainerRuntime = rt

	out, err := Build(in)
	require.NoError(t, err)

	assert.Same(t, rt, out.Config.Containers)
}

// A host-change callback is how the API layer re-ensures its OAuth app after
// an admin changes hosts. If it were dropped, host changes would silently stop
// updating redirect URIs.
func TestBuildPassesTheHostChangeCallback(t *testing.T) {
	called := false
	in := baseInput(t)
	in.OnHostsChanged = func() { called = true }

	out, err := Build(in)
	require.NoError(t, err)

	require.NotNil(t, out.Config.OnHostsChanged)
	out.Config.OnHostsChanged()
	assert.True(t, called, "the callback must reach the orchestrator config intact")
}

func TestBuildRejectsMissingRequiredFields(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Input)
		wantErr string
	}{
		{"logger", func(in *Input) { in.Logger = nil }, "Logger is required"},
		{"db", func(in *Input) { in.DB = nil }, "DB is required"},
		{"registry", func(in *Input) { in.Registry = nil }, "Registry is required"},
		{"catalog cache", func(in *Input) { in.CatalogCache = nil }, "CatalogCache is required"},
		{"tailnet store", func(in *Input) { in.TailnetStore = nil }, "TailnetStore is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := baseInput(t)
			tc.mutate(&in)

			out, err := Build(in)

			require.Error(t, err, "a missing required field must fail the build")
			assert.Nil(t, out)
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.True(t, strings.HasPrefix(err.Error(), "wire:"),
				"the error should name the package that rejected it")
		})
	}
}
