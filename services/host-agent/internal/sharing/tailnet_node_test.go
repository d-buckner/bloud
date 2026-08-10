package sharing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	container "codeberg.org/d-buckner/bloud/services/host-agent/internal/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Fake container.Runtime ──────────────────────────────────────────────────

type FakeRuntime struct {
	Ensured  []container.Spec
	Removed  []string
	Networks []string
}

func (f *FakeRuntime) EnsureNetwork(_ context.Context, name string) error {
	f.Networks = append(f.Networks, name)
	return nil
}

func (f *FakeRuntime) Ensure(_ context.Context, spec container.Spec) (container.EnsureResult, error) {
	f.Ensured = append(f.Ensured, spec)
	return container.EnsureResult{Created: true, Started: true}, nil
}

func (f *FakeRuntime) Remove(_ context.Context, name string) error {
	f.Removed = append(f.Removed, name)
	return nil
}

func (f *FakeRuntime) Inspect(_ context.Context, _ string) (container.State, error) {
	return container.State{}, nil
}

func (f *FakeRuntime) Exec(_ context.Context, _ string, _ []string) error {
	return nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func newTestManager(t *testing.T, rt *FakeRuntime) *TailnetNodeManager {
	t.Helper()
	return NewTailnetNodeManager(rt, nil, func() string { return "tskey-auth-test" }, 8080, t.TempDir(), discardLogger())
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestTailnetNodeContainerName(t *testing.T) {
	assert.Equal(t, "ts-navidrome", TailnetNodeContainerName("navidrome"))
	assert.Equal(t, "ts-jellyfin", TailnetNodeContainerName("jellyfin"))
}

func TestEnsureRunning_CreatesSpecWithServeConfig(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := newTestManager(t, rt)

	err := mgr.EnsureRunning(context.Background(), "navidrome")
	require.NoError(t, err)

	// Verify container spec
	require.Len(t, rt.Ensured, 1)
	spec := rt.Ensured[0]
	assert.Equal(t, "ts-navidrome", spec.Name)
	assert.Equal(t, TailscaleImage, spec.Image)
	assert.Equal(t, []string{"host"}, spec.Networks)
	assert.Equal(t, "tskey-auth-test", spec.Environment["TS_AUTHKEY"])
	assert.Equal(t, "navidrome", spec.Environment["TS_HOSTNAME"])
	assert.Equal(t, "true", spec.Environment["TS_USERSPACE"])
	assert.Equal(t, "/etc/ts-serve/serve.json", spec.Environment["TS_SERVE_CONFIG"])
	assert.Equal(t, "navidrome", spec.Labels["io.bloud.app"])
	assert.Equal(t, "true", spec.Labels["io.bloud.tailnet-node"])
	assert.Equal(t, "always", spec.RestartPolicy)

	// Verify serve config mount and state volume
	require.Len(t, spec.Mounts, 2)
	assert.Equal(t, "/etc/ts-serve", spec.Mounts[0].Destination)
	assert.Equal(t, []string{"ro"}, spec.Mounts[0].Options)
	assert.Equal(t, "/var/lib/tailscale", spec.Mounts[1].Destination)

	// Verify persistent state env vars
	assert.Equal(t, "/var/lib/tailscale", spec.Environment["TS_STATE_DIR"])
	assert.Equal(t, "true", spec.Environment["TS_AUTH_ONCE"])
}

func TestEnsureRunning_WritesServeConfigJSON(t *testing.T) {
	rt := &FakeRuntime{}
	dataDir := t.TempDir()
	mgr := NewTailnetNodeManager(rt, nil, func() string { return "tskey-auth-test" }, 8080, dataDir, discardLogger())

	err := mgr.EnsureRunning(context.Background(), "jellyfin")
	require.NoError(t, err)

	// Read and verify the serve config file
	configPath := filepath.Join(dataDir, "jellyfin", "ts-serve", "serve.json")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	var cfg serveConfig
	require.NoError(t, json.Unmarshal(data, &cfg))

	assert.True(t, cfg.TCP["443"].HTTPS)
	web, ok := cfg.Web["${TS_CERT_DOMAIN}:443"]
	require.True(t, ok, "expected Web entry for ${TS_CERT_DOMAIN}:443")
	assert.Equal(t, "http://localhost:8080", web.Handlers["/"].Proxy)
}

func TestEnsureRunning_Idempotent(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := newTestManager(t, rt)

	require.NoError(t, mgr.EnsureRunning(context.Background(), "navidrome"))
	require.NoError(t, mgr.EnsureRunning(context.Background(), "navidrome"))

	// Ensure was called twice (idempotent, both go through)
	assert.Len(t, rt.Ensured, 2)
}

func TestStop_RemovesContainer(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := newTestManager(t, rt)

	err := mgr.Stop(context.Background(), "navidrome")
	require.NoError(t, err)
	assert.Equal(t, []string{"ts-navidrome"}, rt.Removed)
}

func TestStop_IgnoresNotFound(t *testing.T) {
	rt := &notFoundRuntime{}
	mgr := NewTailnetNodeManager(rt, nil, func() string { return "tskey-auth-test" }, 8080, t.TempDir(), discardLogger())

	err := mgr.Stop(context.Background(), "navidrome")
	require.NoError(t, err)
}

// notFoundRuntime returns "not found" on Remove.
type notFoundRuntime struct{ FakeRuntime }

func (r *notFoundRuntime) Remove(_ context.Context, name string) error {
	return fmt.Errorf("container %s not found", name)
}

// ── Fake ContainerExec ──────────────────────────────────────────────────────

type fakeExec struct {
	output []byte
	err    error
}

func (f *fakeExec) Exec(_ context.Context, _ string, _ []string) ([]byte, error) {
	return f.output, f.err
}

// ── GetAddr Tests ───────────────────────────────────────────────────────────

func TestGetAddr_Success(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{output: []byte("100.64.1.2\n")}
	mgr := NewTailnetNodeManager(rt, exec, func() string { return "tskey-auth-test" }, 8080, t.TempDir(), discardLogger())

	addr, err := mgr.GetAddr(context.Background(), "navidrome")
	require.NoError(t, err)
	assert.Equal(t, "100.64.1.2", addr)
}

func TestGetAddr_ExecError(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{err: fmt.Errorf("container not running")}
	mgr := NewTailnetNodeManager(rt, exec, func() string { return "tskey-auth-test" }, 8080, t.TempDir(), discardLogger())

	_, err := mgr.GetAddr(context.Background(), "navidrome")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "get tailscale addr")
}

func TestGetAddr_EmptyOutput(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{output: []byte("")}
	mgr := NewTailnetNodeManager(rt, exec, func() string { return "tskey-auth-test" }, 8080, t.TempDir(), discardLogger())

	_, err := mgr.GetAddr(context.Background(), "navidrome")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tailscale address")
}

func TestBuildGatewayServeConfig(t *testing.T) {
	cfg := buildGatewayServeConfig(8080)

	assert.True(t, cfg.TCP["443"].HTTPS)

	web := cfg.Web["${TS_CERT_DOMAIN}:443"]
	assert.Equal(t, "http://localhost:8080", web.Handlers["/"].Proxy)
}
