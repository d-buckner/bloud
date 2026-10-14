// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// storeSecrets is an in-memory AppSecretsProvider that keeps what the
// configurator publishes, so the mint-once contract can be exercised across
// passes.
type storeSecrets struct {
	mu      sync.Mutex
	secrets map[string]string
}

func newStoreSecrets() *storeSecrets {
	return &storeSecrets{secrets: map[string]string{}}
}

func (s *storeSecrets) GenerateAppAdminPassword(string) (string, error) { return "admin", nil }

func (s *storeSecrets) GetAppSecret(_, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[key]
}

func (s *storeSecrets) SetAppSecret(_, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[key] = value
	return nil
}

func (s *storeSecrets) SetAppContractValue(_, contract, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[contract+"/"+key] = value
	return nil
}

func (s *storeSecrets) GetAppContractValue(_, contract, key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secrets[contract+"/"+key]
}
func (s *storeSecrets) DeleteAppSecrets(string) error { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testDeps builds a configurator with an in-memory secret store, a static
// base URL, and a recording exec channel. Passing nil for secrets produces a
// genuinely nil AppSecretsProvider, which is the CLI/conformance shape.
func testDeps(secrets configurator.AppSecretsProvider, exec configurator.ExecFunc) configurator.Deps {
	return configurator.Deps{
		Logger:         quietLogger(),
		Secrets:        secrets,
		PrimaryBaseURL: func() string { return "https://bloud.example.com" },
		Exec:           exec,
	}
}

// testState builds the AppState the configurator needs: a data dir, an OIDC
// output, and a wired agentApi provider.
func testState(t *testing.T) *configurator.AppState {
	t.Helper()
	dir := t.TempDir()
	return &configurator.AppState{
		DataPath:      dir,
		BloudDataPath: dir,
		SSOEnabled:    true,
		OIDC: &configurator.OIDCOutput{
			ClientID:     "webui-client",
			ClientSecret: "webui-secret",
			IssuerURL:    "https://sso.example.com/application/o/webui/",
			RedirectURI:  "https://hermes.example.com/api/auth/oidc/callback",
		},
		Integrations: configurator.Integrations{
			AgentAPIs: []configurator.AgentAPIBinding{{
				Endpoint:  "https://hermes.example.com/v1",
				APIKey:    "agent-key",
				ModelName: "hermes",
			}},
		},
	}
}

// TestPreStartMintsAndDeliversTheClientPassword pins the D work item: the
// credential is generated on first pass, persisted under the published secret
// key, and written into the sourced dotenv with the rest of the app config.
func TestPreStartMintsAndDeliversTheClientPassword(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := NewConfigurator(0, testDeps(secrets, nil))
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)

	minted := secrets.GetAppSecret(appName, clientPasswordSecret)
	require.NotEmpty(t, minted)

	// The data dir must be world-writable for the container's uid-1024 runtime
	// user (the image verifies it by touching a test file there).
	info, err := os.Stat(filepath.Join(state.DataPath, "data"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o777), info.Mode().Perm())

	// The dotenv carries the value, the gateway wiring, and the OIDC client.
	content, err := os.ReadFile(filepath.Join(state.DataPath, "config", envFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), "HERMES_WEBUI_PASSWORD='"+minted+"'")
	assert.Contains(t, string(content), "HERMES_WEBUI_CHAT_BACKEND='gateway'")
	assert.Contains(t, string(content), "HERMES_WEBUI_GATEWAY_BASE_URL='https://hermes.example.com'")
	assert.Contains(t, string(content), "HERMES_WEBUI_DEFAULT_MODEL='hermes'")
	assert.Contains(t, string(content), "HERMES_WEBUI_OIDC_CLIENT_ID='webui-client'")
	assert.Contains(t, string(content), "HERMES_WEBUI_SECURE='true'")
}

// TestPreStartIsIdempotentAcrossResync pins the invariant-2 contract: a second
// pass with the same inputs mints nothing new, rewrites nothing, and asks for
// no recreate.
func TestPreStartIsIdempotentAcrossResync(t *testing.T) {
	secrets := newStoreSecrets()
	cfg := NewConfigurator(0, testDeps(secrets, nil))
	state := testState(t)

	first, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	require.True(t, first.RestartNeeded, "the first write must ask for the recreate")

	firstMinted := secrets.GetAppSecret(appName, clientPasswordSecret)

	second, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, second.RestartNeeded, "a steady-state resync must not ask for a recreate")
	assert.Equal(t, firstMinted, secrets.GetAppSecret(appName, clientPasswordSecret),
		"the password is minted once, not per pass")
}

// TestPreStartWithoutSecretsStillWritesConfig pins the nil-store degradation
// the conformance harness relies on: with no store, the app config is written
// on its OIDC path alone rather than failing the pass.
func TestPreStartWithoutSecretsStillWritesConfig(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := testState(t)

	_, err := cfg.PreStart(context.Background(), state)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(state.DataPath, "config", envFileName))
	require.NoError(t, err)
	assert.NotContains(t, string(content), "HERMES_WEBUI_PASSWORD=",
		"no password is written when there is no store to hold it")
	assert.Contains(t, string(content), "HERMES_WEBUI_OIDC_CLIENT_ID='webui-client'")
}

// TestPrepareDataDirWidensAnAgentOwnedDir pins the first-boot contract: a
// directory the agent still owns gets widened to 0777, because the container's
// uid-1024 runtime needs the world-write bit to write its state into a path
// the agent created.
func TestPrepareDataDirWidensAnAgentOwnedDir(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir()}

	require.NoError(t, cfg.prepareDataDir(state))

	info, err := os.Stat(filepath.Join(state.DataPath, "data"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o777), info.Mode().Perm())
}

// TestPrepareDataDirLeavesAWidenedDirUntouched pins that a pass does not
// chmod a directory that already carries the mode, so it cannot narrow what
// the app or an operator set, and a steady-state resync stays a read-only diff.
func TestPrepareDataDirLeavesAWidenedDirUntouched(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir()}

	require.NoError(t, cfg.prepareDataDir(state))
	dir := filepath.Join(state.DataPath, "data")
	before, err := os.Stat(dir)
	require.NoError(t, err)

	require.NoError(t, cfg.prepareDataDir(state))
	after, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, before.ModTime().Equal(after.ModTime()),
		"an already-writable dir must not be chmodded again")
}

// TestPrepareDataDirReportsACreateFailure keeps the two failure modes
// distinguishable: a parent that is not a directory is a broken install and
// must read as "create data dir", never as the tolerated cross-uid chmod case.
func TestPrepareDataDirReportsACreateFailure(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o644))
	state := &configurator.AppState{DataPath: parent}

	err := cfg.prepareDataDir(state)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create data dir")
	assert.NotContains(t, err.Error(), "make data dir writable")
}

// foreignOwnedDataDir reproduces the takeover the issue describes: the data
// directory owned by the container's runtime uid at a mode short of 0777.
// Under rootless podman, container uid 1024 lands on host uid 101023 with a
// 100000-based mapping, so the test user is neither owner nor group member and
// os.Chmod on the directory returns EPERM. That is the exact condition the
// agent hits on a live install, and no single-uid test can manufacture it:
// tightening the mode does not help, because the owner may always chmod its own
// directory. A second uid is required, hence the container.
//
// Skips rather than fails when podman or the image is unavailable. The
// tolerance decision itself stays pinned by managedfile's containerOwned table;
// what this adds is the end-to-end path through prepareDataDir.
func foreignOwnedDataDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("podman"); err != nil {
		t.Skip("podman unavailable: cannot create a cross-uid directory to reproduce EPERM")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("podman", "run", "--rm", "-u", "0",
		"-v", dir+":/host:z", "alpine",
		"sh", "-c", "chown 1024:1024 /host && chmod 0750 /host")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("podman cannot create a cross-uid directory (%v): %s", err, out)
	}

	info, err := os.Stat(dir)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "no Stat_t behind the directory's FileInfo")
	if st.Uid == uint32(os.Getuid()) {
		t.Skip("podman mapped the container uid onto our own; no cross-uid condition to test")
	}
	// The setup only means something if the chmod really is refused.
	if err := os.Chmod(dir, 0o777); err == nil {
		t.Skip("the host user could chmod the foreign directory; no EPERM to tolerate")
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("expected a permission error from the foreign chmod, got %v", err)
	}
	return dir
}

// TestPrepareDataDirToleratesAContainerOwnedDir is the regression test for the
// issue: once the image's init chowns its state directory, the agent's chmod
// is refused forever. prepareDataDir has to converge anyway, because a failure
// here is what parked fresh installs in ERROR before the client password was
// minted and left running apps serving a permanently stale bloud.env. Fails
// against a bare os.Chmod, passes through managedfile.EnsureWritable.
func TestPrepareDataDirToleratesAContainerOwnedDir(t *testing.T) {
	dir := foreignOwnedDataDir(t)
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: filepath.Dir(dir)}

	require.NoError(t, cfg.prepareDataDir(state))

	// The tolerance must not come from fighting the takeover: the directory is
	// still the container's, still 0750, still written by the uid that owns it.
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), info.Mode().Perm(),
		"the fix tolerates the takeover rather than clawing the directory back")
}

// TestRevokeSessionsClearsTheStoreThroughExec pins G: revocation runs the
// removal inside the container, not against the host path.
func TestRevokeSessionsClearsTheStoreThroughExec(t *testing.T) {
	var execCalls []string
	exec := func(_ context.Context, container string, _ map[string]string, cmd []string) ([]byte, error) {
		execCalls = append(execCalls, container+":"+strings.Join(cmd, " "))
		return nil, nil
	}

	cfg := NewConfigurator(0, testDeps(nil, exec))
	err := cfg.RevokeSessions(context.Background(), testState(t))
	require.NoError(t, err)

	require.Len(t, execCalls, 1)
	assert.Equal(t, "apps-hermes-webui:rm -f /data/.sessions.json", execCalls[0])
}

// TestRevokeSessionsRequiresAnExecChannel pins the failure mode: without the
// host runtime callback there is no way to reach the container, and pretending
// the sessions were cleared would be a lie.
func TestRevokeSessionsRequiresAnExecChannel(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	err := cfg.RevokeSessions(context.Background(), testState(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no exec channel")
}

// TestRenderEnvOmitsEmptyValues pins writeEnv's "unset, not empty" rule: the
// app treats an absent variable as unconfigured and an empty one as configured
// with nothing, which are different states.
func TestRenderEnvOmitsEmptyValues(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir()}

	out := cfg.renderEnv(agentConfig{}, state, "")
	assert.NotContains(t, out, "HERMES_WEBUI_PASSWORD")
	assert.NotContains(t, out, "HERMES_WEBUI_GATEWAY_BASE_URL")
	assert.NotContains(t, out, "HERMES_WEBUI_OIDC_CLIENT_ID")
}

// TestGatewayRootStripsTheOpenAIPath pins the double-/v1 bug: the webui appends
// /v1/chat/completions to HERMES_WEBUI_GATEWAY_BASE_URL, so the binding's
// OpenAI base (which ends in /v1) would double the prefix. The gateway root is
// the origin with that final segment removed, and the origin is never touched.
func TestGatewayRootStripsTheOpenAIPath(t *testing.T) {
	assert.Equal(t, "https://hermes.example.com", gatewayRoot("https://hermes.example.com/v1"))
	assert.Equal(t, "https://hermes.example.com", gatewayRoot("https://hermes.example.com/v1/"))
	assert.Equal(t, "http://hermes.localhost:8080", gatewayRoot("http://hermes.localhost:8080/v1"))
	// An origin with no path is returned unchanged, so a prefix-less endpoint
	// cannot be mangled.
	assert.Equal(t, "https://hermes.example.com", gatewayRoot("https://hermes.example.com"))
	// A bare origin with a port must not have the port mistaken for a path.
	assert.Equal(t, "https://hermes.example.com:8080", gatewayRoot("https://hermes.example.com:8080"))
}

// TestPrepareInboxCreatesAndWidensTheSharedTree: the orchestrator creates every
// mount source 0755 owned by the host agent. This app's runtime user is uid 1024,
// which is neither that owner nor a group member, so without the world-write bit
// every upload fails with EACCES inside a mount that exists.
func TestPrepareInboxCreatesAndWidensTheSharedTree(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir(), BloudDataPath: t.TempDir()}

	require.NoError(t, cfg.prepareInbox(state))

	info, err := os.Stat(filepath.Join(state.BloudDataPath, sharedInboxRel))
	require.NoError(t, err)
	// Asserted against the literal, not against sharedInboxMode: the constant is
	// duplicated in apps/hermes, and this is what fails if either half drifts.
	assert.Equal(t, os.FileMode(0o1777), inboxMode(info),
		"the world bits are the only ones that reach the agent's uid, and the sticky "+
			"bit is what stops this app's own delete API from unlinking someone else's upload")
}

// TestPrepareInboxRepairsANarrowedInbox is the assertion the steady-state case
// cannot make. A chmod does not move mtime, so "it did not chmod again" is not
// observable from a test; what matters and is observable is that a directory that
// lost its bits gets them back. The orchestrator creates every mount source 0755
// (orchestrator/execution.go, ensureMountDirs), so this is not a hypothetical.
func TestPrepareInboxRepairsANarrowedInbox(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))
	state := &configurator.AppState{DataPath: t.TempDir(), BloudDataPath: t.TempDir()}

	require.NoError(t, cfg.prepareInbox(state))
	dir := filepath.Join(state.BloudDataPath, sharedInboxRel)
	require.NoError(t, os.Chmod(dir, 0o755))

	require.NoError(t, cfg.prepareInbox(state))
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o1777), inboxMode(info))
}

// inboxMode folds the permission bits and the sticky bit into one number, because
// FileInfo.Mode().Perm() masks the sticky bit away and a test that compares only
// Perm() cannot tell 0777 from 1777.
func inboxMode(info os.FileInfo) os.FileMode {
	m := info.Mode().Perm()
	if info.Mode()&os.ModeSticky != 0 {
		m |= 0o1000
	}
	return m
}

// TestPrepareInboxWithoutASharedRootIsNotFatal keeps a pass with no shared root
// succeeding: a missing root is an offline or CLI context, and a node parked in
// ERROR over a directory only uploads need is worse than an app whose config was
// still written. It must not fall back to a relative path, though, or it litters
// the host agent's working directory.
func TestPrepareInboxWithoutASharedRootIsNotFatal(t *testing.T) {
	cfg := NewConfigurator(0, testDeps(nil, nil))

	require.NoError(t, cfg.prepareInbox(&configurator.AppState{DataPath: t.TempDir()}))
	if _, err := os.Stat(sharedInboxRel); !os.IsNotExist(err) {
		t.Errorf("an empty shared root created %q relative to the working directory", sharedInboxRel)
	}
}

// TestMetadata_DeclaresTheSharedInbox pins this app's constant to the mount it
// declares. The Go side widens the path built from the constant; the YAML side is
// what podman mounts. Drift would widen a directory nobody mounts while the real
// inbox stays unwritable.
func TestMetadata_DeclaresTheSharedInbox(t *testing.T) {
	raw, err := os.ReadFile("metadata.yaml")
	if err != nil {
		t.Fatalf("reading metadata.yaml: %v", err)
	}
	var m struct {
		Containers []struct {
			Name    string `yaml:"name"`
			Volumes []struct {
				Source      string `yaml:"source"`
				Destination string `yaml:"destination"`
			} `yaml:"volumes"`
		} `yaml:"containers"`
	}
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("parsing metadata.yaml: %v", err)
	}

	var found int
	for _, c := range m.Containers {
		for _, v := range c.Volumes {
			if v.Source == "{{dataDir}}/"+sharedInboxRel {
				found++
				assert.Equal(t, "/bloud-hermes/inbox", v.Destination,
					"the inbox must mount where Hermes mounts it, or the path this app "+
						"hands the agent resolves to nothing in the agent's container")
			}
		}
	}
	require.Equal(t, 1, found, "expected exactly one mount of {{dataDir}}/%s", sharedInboxRel)
}
