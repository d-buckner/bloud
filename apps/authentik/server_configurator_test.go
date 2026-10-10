// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authentikClient "codeberg.org/d-buckner/bloud/services/host-agent/pkg/authentik"
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

// The Django shells are the bootstrap, not the steady state. A server that
// accepts Bloud's token must be diffed over the API: the spawn costs seconds and
// the reconciler would pay it on every pass forever (issue #306).
func TestServerConfigurator_EnsureAPIToken_NoShellWhenTheTokenWorks(t *testing.T) {
	execs := 0
	srv := stubTokenServer(t, `[{"identifier":"bloud-api-token","user_obj":{"pk":2,"is_superuser":true}}]`, "the-key")
	c := NewServerConfigurator(configurator.Deps{
		Exec: func(context.Context, string, map[string]string, []string) ([]byte, error) {
			execs++
			return []byte("OK"), nil
		},
	}, Params{Port: serverPort(t, srv), TokenKey: "the-key"})

	require.NoError(t, c.ensureAPIToken(context.Background(),
		authentikClient.NewClient(srv.URL, "the-key")))
	assert.Zero(t, execs, "a token that works needs no Django shell")
}

// The other half: the shell is the only way in when the credential the API
// authenticates with does not exist yet, or was rotated or deleted by hand.
func TestServerConfigurator_EnsureAPIToken_FallsBackToTheShell(t *testing.T) {
	var scripts []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := NewServerConfigurator(configurator.Deps{
		Exec: func(_ context.Context, _ string, _ map[string]string, cmd []string) ([]byte, error) {
			scripts = append(scripts, cmd[len(cmd)-1])
			return []byte("OK"), nil
		},
	}, Params{Port: serverPort(t, srv), TokenKey: "the-key"})

	require.NoError(t, c.ensureAPIToken(context.Background(), authentikClient.NewClient(srv.URL, "the-key")))
	assert.Equal(t, []string{setAdminPasswordScript, ensureAPITokenScript}, scripts,
		"both halves of the bootstrap run, in the order that leaves the API usable")
}

// serverPort extracts the port an httptest server is listening on.
func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	require.NoError(t, err)
	n, err := strconv.Atoi(port)
	require.NoError(t, err)
	return n
}

// stubTokenServer answers the two token calls EnsureAPIToken makes.
func stubTokenServer(t *testing.T, tokens, key string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/view_key/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"key":"` + key + `"}`))
		case r.URL.Path == "/api/v3/core/tokens/":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"results":` + tokens + `}`))
		default:
			t.Errorf("unexpected call: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
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
