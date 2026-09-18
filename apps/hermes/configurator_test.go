// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package hermes

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakeSecrets returns a fixed admin password and records the app key it was
// asked for, so the credential contract (generated under "hermes") is
// observable.
type fakeSecrets struct {
	pw     string
	called []string
}

func (f *fakeSecrets) GenerateAppAdminPassword(app string) (string, error) {
	f.called = append(f.called, app)
	return f.pw, nil
}

func (f *fakeSecrets) GetAppSecret(_, _ string) string { return "" }

func TestName(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	assert.Equal(t, "apps-hermes", c.Name())
}

func TestDefaultPort(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	assert.Equal(t, 9119, c.Port, "zero port falls back to the upstream dashboard default")

	c = NewConfigurator(9999, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	assert.Equal(t, 9999, c.Port)
}

// TestPreStartResolvesCredentialUnderHermesKey: the credential must be
// generated under the catalog ID "hermes" so the same value the container
// renders (appSecrets.hermes.adminPassword) is the one PreStart validated.
// PreStart manages no files, so changed must be false.
func TestPreStartResolvesCredentialUnderHermesKey(t *testing.T) {
	secrets := &fakeSecrets{pw: "generated-pw"}
	c := NewConfigurator(0, configurator.Deps{Secrets: secrets, Logger: quietLogger()})

	changed, err := c.PreStart(context.Background(), &configurator.AppState{})
	require.NoError(t, err)
	assert.False(t, changed, "PreStart writes no mounted file, so it never signals a restart")
	assert.Equal(t, []string{"hermes"}, secrets.called)
}

// TestPreStartRequiresSecretsProvider: without a secrets provider the
// dashboard would boot fail-closed (empty password). PreStart must refuse
// rather than let a misconfigured install stand.
func TestPreStartRequiresSecretsProvider(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Logger: quietLogger()})
	_, err := c.PreStart(context.Background(), &configurator.AppState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "secrets provider")
}

// TestPreStartRejectsEmptyPassword: an empty password from the provider is
// rejected — it would leave the auth gate unable to start.
func TestPreStartRejectsEmptyPassword(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: ""}, Logger: quietLogger()})
	_, err := c.PreStart(context.Background(), &configurator.AppState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestPostStartSucceedsWhenHealthy: /api/health answering 200 satisfies the
// wait. Confirms the probe targets the health path (not an auth-gated one).
func TestPostStartSucceedsWhenHealthy(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	c.baseURL = srv.URL

	require.NoError(t, c.PostStart(context.Background(), &configurator.AppState{}))
	assert.Equal(t, "/api/health", gotPath)
}

// TestPostStartFailsWhenUnreachable: with the health endpoint never
// returning ready within the ctx budget, PostStart surfaces an error
// (dashboard not up) rather than reporting success.
func TestPostStartFailsWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	t.Cleanup(cancel)

	err := c.PostStart(ctx, &configurator.AppState{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not reachable")
}

// TestRemoveIsNoOp documents that teardown is the orchestrator's job.
func TestRemoveIsNoOp(t *testing.T) {
	c := NewConfigurator(0, configurator.Deps{Secrets: &fakeSecrets{pw: "x"}, Logger: quietLogger()})
	require.NoError(t, c.Remove(context.Background(), &configurator.AppState{}, true))
	require.NoError(t, c.Remove(context.Background(), &configurator.AppState{}, false))
}

// TestConfiguratorSatisfiesNodeLifecycle guards the interface implementation
// at compile time without a runtime assertion.
var _ configurator.NodeLifecycle = (*Configurator)(nil)
