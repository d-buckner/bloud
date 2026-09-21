// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeCacheApp(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	meta := fmt.Sprintf("name: %s\ndisplayName: %s\ndescription: race-test app\ncategory: media\nport: 8080\n", name, name)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metadata.yaml"), []byte(meta), 0o644))
}

// Refresh mutates the cache map while orchestrator goroutines read it from
// graph event handlers and the API serves catalog requests. Without
// synchronization the Go runtime raises the unrecoverable
// "fatal error: concurrent map read and map write"; under -race the same
// interleaving is a hard failure. Run in the race tier
// (validation.yaml: go-host-agent-race covers ./internal/catalog/...).
func TestMemoryCache_ConcurrentRefreshAndReads(t *testing.T) {
	root := t.TempDir()
	writeCacheApp(t, root, "alpha")
	writeCacheApp(t, root, "beta")
	loader := NewLoader(root)

	c := NewMemoryCache()
	require.NoError(t, c.Refresh(loader))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_, _ = c.Get("alpha")
				_, _ = c.GetAll()
				_, _ = c.GetUserApps()
				_ = c.IsSystemAppByName("beta")
			}
		}()
	}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, c.Refresh(loader))
		}()
	}
	wg.Wait()

	// Every writer completed, so the cache ends on one whole refresh:
	// never a torn or half-filled map.
	apps, err := c.GetAll()
	require.NoError(t, err)
	require.Len(t, apps, 2)
}
