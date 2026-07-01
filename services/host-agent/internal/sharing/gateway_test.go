package sharing

import (
	"context"
	"fmt"
	"testing"

	container "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGateway_EnsureRunning_CreatesContainerSpec(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	err := mgr.EnsureRunning(context.Background())
	require.NoError(t, err)

	require.Len(t, rt.Ensured, 1)
	spec := rt.Ensured[0]

	assert.Equal(t, "ts-gateway", spec.Name)
	assert.Equal(t, "docker.io/tailscale/tailscale:latest", spec.Image)
	assert.Equal(t, "host", spec.Network)
	assert.Equal(t, "tskey-auth-gw", spec.Environment["TS_AUTHKEY"])
	assert.Equal(t, "bloud", spec.Environment["TS_HOSTNAME"])
	assert.Equal(t, "true", spec.Environment["TS_USERSPACE"])
	assert.Equal(t, ":1055", spec.Environment["TS_SOCKS5_SERVER"])
	assert.Equal(t, "--accept-routes", spec.Environment["TS_EXTRA_ARGS"])
	assert.Equal(t, "true", spec.Labels["io.bloud.gateway"])
	assert.Equal(t, "always", spec.RestartPolicy)

	// Gateway is host-network, no DependsOn
	assert.Empty(t, spec.DependsOn)

	// TS_SERVE_CONFIG for Tailscale Serve (HTTPS on 443 → Traefik)
	assert.Equal(t, "/etc/ts-serve/serve.json", spec.Environment["TS_SERVE_CONFIG"])

	// Mounts: state volume + serve config (ro)
	require.Len(t, spec.Mounts, 2)
	assert.Equal(t, "/var/lib/tailscale", spec.Mounts[0].Destination)
	assert.Equal(t, "/etc/ts-serve", spec.Mounts[1].Destination)
	assert.Equal(t, []string{"ro"}, spec.Mounts[1].Options)
	assert.Equal(t, "/var/lib/tailscale", spec.Environment["TS_STATE_DIR"])
	assert.Equal(t, "true", spec.Environment["TS_AUTH_ONCE"])
}

func TestGateway_EnsureRunning_NoAuthKey(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "" }, 1055, 8080, t.TempDir(), discardLogger())

	err := mgr.EnsureRunning(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no tailnet connection configured")
	assert.Empty(t, rt.Ensured)
}

func TestGateway_EnsureRunning_Idempotent(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	require.NoError(t, mgr.EnsureRunning(context.Background()))
	require.NoError(t, mgr.EnsureRunning(context.Background()))

	assert.Len(t, rt.Ensured, 2)
}

func TestGateway_EnsureRunning_CustomSocksPort(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 2080, 8080, t.TempDir(), discardLogger())

	require.NoError(t, mgr.EnsureRunning(context.Background()))

	require.Len(t, rt.Ensured, 1)
	assert.Equal(t, ":2080", rt.Ensured[0].Environment["TS_SOCKS5_SERVER"])
}

func TestGateway_Stop_RemovesContainer(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	err := mgr.Stop(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"ts-gateway"}, rt.Removed)
}

func TestGateway_Stop_IgnoresNotFound(t *testing.T) {
	rt := &notFoundRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	err := mgr.Stop(context.Background())
	require.NoError(t, err)
}

func TestGateway_IsRunning_True(t *testing.T) {
	rt := &inspectableRuntime{state: container.State{Exists: true, Running: true}}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	assert.True(t, mgr.IsRunning(context.Background()))
}

func TestGateway_IsRunning_False_NotRunning(t *testing.T) {
	rt := &inspectableRuntime{state: container.State{Exists: true, Running: false}}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	assert.False(t, mgr.IsRunning(context.Background()))
}

func TestGateway_IsRunning_False_NotExist(t *testing.T) {
	rt := &inspectableRuntime{err: fmt.Errorf("no such container")}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	assert.False(t, mgr.IsRunning(context.Background()))
}

// inspectableRuntime is a FakeRuntime that returns configurable Inspect results.
type inspectableRuntime struct {
	FakeRuntime
	state container.State
	err   error
}

func (r *inspectableRuntime) Inspect(_ context.Context, _ string) (container.State, error) {
	return r.state, r.err
}

// ── GetTailnetDomain Tests ──────────────────────────────────────────────────

func TestGateway_GetTailnetDomain_Success(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{output: []byte(`{"MagicDNSSuffix":"tail12756a.ts.net."}`)}
	mgr := NewGatewayManager(rt, exec, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	domain, err := mgr.GetTailnetDomain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tail12756a.ts.net", domain)
}

func TestGateway_GetTailnetDomain_NoTrailingDot(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{output: []byte(`{"MagicDNSSuffix":"tail12756a.ts.net"}`)}
	mgr := NewGatewayManager(rt, exec, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	domain, err := mgr.GetTailnetDomain(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tail12756a.ts.net", domain)
}

func TestGateway_GetTailnetDomain_ExecError(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{err: fmt.Errorf("container not running")}
	mgr := NewGatewayManager(rt, exec, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	_, err := mgr.GetTailnetDomain(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exec tailscale status in gateway")
}

func TestGateway_GetTailnetDomain_EmptySuffix(t *testing.T) {
	rt := &FakeRuntime{}
	exec := &fakeExec{output: []byte(`{"MagicDNSSuffix":""}`)}
	mgr := NewGatewayManager(rt, exec, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	_, err := mgr.GetTailnetDomain(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MagicDNSSuffix is empty")
}

func TestGateway_GetTailnetDomain_NilExec(t *testing.T) {
	rt := &FakeRuntime{}
	mgr := NewGatewayManager(rt, nil, func() string { return "tskey-auth-gw" }, 1055, 8080, t.TempDir(), discardLogger())

	_, err := mgr.GetTailnetDomain(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "container exec not available")
}
