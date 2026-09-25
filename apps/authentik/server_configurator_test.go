// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func writeBlueprint(t *testing.T, appsDir, content string) {
	t.Helper()
	dir := filepath.Join(appsDir, "authentik")
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.yaml"), []byte(content), 0644))
}

func TestServerConfigurator_PreStart_CopiesBlueprintOnlyWhenChanged(t *testing.T) {
	appsDir := t.TempDir()
	writeBlueprint(t, appsDir, "version: 1\n")
	dataPath := t.TempDir()

	c := NewServerConfigurator(configurator.Deps{}, Params{AppsDir: appsDir})
	state := &configurator.AppState{DataPath: dataPath}

	changed, err := c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "first copy must report a change")

	got, err := os.ReadFile(filepath.Join(dataPath, "authentik-auth-flow.yaml"))
	require.NoError(t, err)
	assert.Equal(t, "version: 1\n", string(got))

	// Idempotent: an unchanged blueprint must not report a change, or the
	// orchestrator would recreate the container every reconciliation pass.
	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.False(t, changed.RestartNeeded, "unchanged blueprint must not report a change")

	// A changed source blueprint is picked up.
	writeBlueprint(t, appsDir, "version: 2\n")
	changed, err = c.PreStart(context.Background(), state)
	require.NoError(t, err)
	assert.True(t, changed.RestartNeeded, "changed blueprint must report a change")
}

func TestServerConfigurator_RunDjangoShell_UsesDepsExec(t *testing.T) {
	var gotName string
	var gotEnv map[string]string
	var gotCmd []string
	deps := configurator.Deps{
		Exec: func(_ context.Context, name string, env map[string]string, cmd []string) ([]byte, error) {
			gotName, gotEnv, gotCmd = name, env, cmd
			return []byte("OK"), nil
		},
	}
	c := NewServerConfigurator(deps, Params{})

	env := map[string]string{"BLOUD_ADMIN_PASSWORD": "pw"}
	require.NoError(t, c.runDjangoShell(context.Background(), env, setAdminPasswordScript))

	assert.Equal(t, "apps-authentik-server", gotName)
	assert.Equal(t, "pw", gotEnv["BLOUD_ADMIN_PASSWORD"])
	assert.Equal(t, []string{"ak", "shell", "-c", setAdminPasswordScript}, gotCmd)
}

func TestServerConfigurator_RunDjangoShell_NoRuntimeErrors(t *testing.T) {
	c := NewServerConfigurator(configurator.Deps{}, Params{})
	require.Error(t, c.runDjangoShell(context.Background(), nil, "print('x')"),
		"a nil Deps.Exec must fail clearly, not panic")
}

func TestServerConfigurator_RunDjangoShell_FailsWithoutOK(t *testing.T) {
	deps := configurator.Deps{
		Exec: func(context.Context, string, map[string]string, []string) ([]byte, error) {
			return []byte("ERROR: boom"), nil
		},
	}
	c := NewServerConfigurator(deps, Params{})
	require.Error(t, c.runDjangoShell(context.Background(), nil, "script"))
}
