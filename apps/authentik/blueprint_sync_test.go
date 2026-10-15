// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authentikClient "codeberg.org/d-buckner/bloud/pkg/authentik"
	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

func sha512Hex(content string) string {
	sum := sha512.Sum512([]byte(content))
	return hex.EncodeToString(sum[:])
}

// blueprintSyncFixture stands in for Authentik's blueprint API and the worker
// container. applied is the hash Authentik reports; mounted is what the worker
// sees through its bind mount. A worker restart re-reads the mount and applies
// it, as the real worker does on startup.
type blueprintSyncFixture struct {
	applied  []string // successive answers; the last one repeats
	mounted  string
	restarts []string
	execs    int
}

func (f *blueprintSyncFixture) run(t *testing.T, content string) error {
	t.Helper()
	dataPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, authFlowFile), []byte(content), 0644))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v3/managed/blueprints/", r.URL.Path)
		assert.Equal(t, authFlowBlueprintPath, r.URL.Query().Get("path"))
		hash := f.applied[0]
		if len(f.applied) > 1 {
			f.applied = f.applied[1:]
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"last_applied_hash": hash, "status": "successful"}},
		})
	}))
	t.Cleanup(srv.Close)

	c := NewServerConfigurator(configurator.Deps{
		Runtime: configurator.RuntimeDeps{
			Exec: func(_ context.Context, name string, _ map[string]string, cmd []string) ([]byte, error) {
				f.execs++
				assert.Equal(t, workerNode, name)
				assert.Equal(t, []string{"sha512sum", "/blueprints/" + authFlowBlueprintPath}, cmd)
				return []byte(f.mounted + "  /blueprints/" + authFlowBlueprintPath + "\n"), nil
			},
			RestartContainer: func(_ context.Context, name string) error {
				f.restarts = append(f.restarts, name)
				f.mounted = sha512Hex(content)
				f.applied = []string{f.mounted}
				return nil
			},
		},
	}, Params{})
	client := authentikClient.NewClient(srv.URL, "test-token")
	return c.ensureAuthFlowApplied(context.Background(), client, dataPath)
}

func TestEnsureAuthFlowApplied_SteadyStateIsOneRead(t *testing.T) {
	f := &blueprintSyncFixture{applied: []string{sha512Hex("current")}}

	require.NoError(t, f.run(t, "current"))
	assert.Zero(t, f.execs, "an applied blueprint needs no look inside the worker")
	assert.Empty(t, f.restarts)
}

// The bug this exists for: PreStart rewrote the file, the worker's single-file
// mount still shows the old inode, and nothing would ever apply the new content.
func TestEnsureAuthFlowApplied_RestartsAWorkerOnAStaleMount(t *testing.T) {
	f := &blueprintSyncFixture{applied: []string{sha512Hex("old")}, mounted: sha512Hex("old")}

	require.NoError(t, f.run(t, "current"))
	assert.Equal(t, []string{workerNode}, f.restarts)
}

// A cold install: the worker started on the current file and has not applied it
// yet. Restarting it would only start the wait over.
func TestEnsureAuthFlowApplied_WaitsOnAWorkerThatSeesTheCurrentFile(t *testing.T) {
	f := &blueprintSyncFixture{applied: []string{"", sha512Hex("current")}, mounted: sha512Hex("current")}

	require.NoError(t, f.run(t, "current"))
	assert.Empty(t, f.restarts)
}
