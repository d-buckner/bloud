// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCache stands in for the catalog cache so the shared-tree ownership
// rules can be exercised without a loader or a disk catalog.
type fakeCache struct {
	apps map[string]*App
}

func (f fakeCache) Get(name string) (*App, error) {
	app, ok := f.apps[name]
	if !ok {
		return nil, fmt.Errorf("app %q not found", name)
	}
	return app, nil
}

func (f fakeCache) GetAll() ([]*App, error) {
	out := make([]*App, 0, len(f.apps))
	for _, a := range f.apps {
		out = append(out, a)
	}
	return out, nil
}

func (f fakeCache) GetUserApps() ([]*App, error) { return f.GetAll() }

func (f fakeCache) IsSystemAppByName(string) bool { return false }

func (f fakeCache) Refresh(*Loader) error { return nil }

func appWithVolumes(name string, owns []string, volumes ...[2]string) *App {
	app := &App{CatalogID: name, OwnsSharedData: owns}
	for i, v := range volumes {
		app.Containers = append(app.Containers, ContainerDef{
			Name:    fmt.Sprintf("%s-c%d", name, i),
			Image:   "example:1.0",
			Volumes: []ContainerVolume{{Source: v[0], Destination: v[1]}},
		})
	}
	return app
}

func TestSharedDataToClear_OwnedTreeUnshared_IsRemoved(t *testing.T) {
	cache := fakeCache{apps: map[string]*App{
		"hermes": appWithVolumes("hermes",
			[]string{"{{dataDir}}/hermes/home"},
			[2]string{"{{dataDir}}/hermes/home", "/opt/data"}),
	}}

	got, err := SharedDataToClear(cache, []string{"hermes"}, "/data", "hermes")
	require.NoError(t, err)
	assert.Equal(t, []string{"/data/hermes/home"}, got,
		"the tree the app owns and nobody else mounts is cleared with it")
}

func TestSharedDataToClear_TreeStillMountedElsewhere_IsLeftAlone(t *testing.T) {
	// The owner claims the tree, but another installed app still mounts it.
	// Clearing the owner would destroy that app's state, so the claim loses.
	cache := fakeCache{apps: map[string]*App{
		"hermes": appWithVolumes("hermes",
			[]string{"{{dataDir}}/hermes/home"},
			[2]string{"{{dataDir}}/hermes/home", "/opt/data"}),
		"someone-else": appWithVolumes("someone-else", nil,
			[2]string{"{{dataDir}}/hermes/home", "/mnt/brain"}),
	}}

	got, err := SharedDataToClear(cache, []string{"hermes", "someone-else"}, "/data", "hermes")
	require.NoError(t, err)
	assert.Empty(t, got, "a shared tree another installed app mounts is never cleared")
}

func TestSharedDataToClear_OwnerOnlyMountsItsOwnPrivateTree(t *testing.T) {
	// The owner's own apps/<name> tree is not a shared tree and is not
	// reported here; the ordinary clearData path removes it.
	cache := fakeCache{apps: map[string]*App{
		"plain": appWithVolumes("plain", nil,
			[2]string{"{{appDataDir}}/data", "/data"}),
	}}

	got, err := SharedDataToClear(cache, []string{"plain"}, "/data", "plain")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSharedDataToClear_UninstalledConsumerDoesNotProtectTheTree(t *testing.T) {
	// A catalog app that mounts the tree but is not installed cannot keep it
	// alive: only installed mounts count.
	cache := fakeCache{apps: map[string]*App{
		"hermes": appWithVolumes("hermes",
			[]string{"{{dataDir}}/hermes/home"},
			[2]string{"{{dataDir}}/hermes/home", "/opt/data"}),
		"not-installed": appWithVolumes("not-installed", nil,
			[2]string{"{{dataDir}}/hermes/home", "/mnt/brain"}),
	}}

	got, err := SharedDataToClear(cache, []string{"hermes"}, "/data", "hermes")
	require.NoError(t, err)
	assert.Equal(t, []string{"/data/hermes/home"}, got)
}

func TestSharedDataToClear_AppDataDirEntryIsRejected(t *testing.T) {
	// ownsSharedData is for trees outside apps/. Naming the app's own private
	// tree would be a declaration that means something other than it says.
	cache := fakeCache{apps: map[string]*App{
		"bad": appWithVolumes("bad", []string{"{{appDataDir}}/data"}),
	}}

	_, err := SharedDataToClear(cache, []string{"bad"}, "/data", "bad")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "private tree")
}

func TestSharedDataToClear_PathOutsideDataRootIsRejected(t *testing.T) {
	for _, declared := range []string{"/var/lib", "{{dataDir}}/../escape", "{{dataDir}}"} {
		cache := fakeCache{apps: map[string]*App{
			"bad": appWithVolumes("bad", []string{declared}),
		}}

		_, err := SharedDataToClear(cache, []string{"bad"}, "/data", "bad")
		require.Error(t, err, "entry %q must not be accepted", declared)
		assert.Contains(t, err.Error(), "outside the data root")
	}
}

func TestSharedDataToClear_NoDeclarationIsANoop(t *testing.T) {
	cache := fakeCache{apps: map[string]*App{
		"plain": appWithVolumes("plain", nil),
	}}

	got, err := SharedDataToClear(cache, []string{"plain"}, "/data", "plain")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSharedDataToClear_NilCacheOrDataDirIsANoop(t *testing.T) {
	got, err := SharedDataToClear(nil, nil, "/data", "hermes")
	require.NoError(t, err)
	assert.Empty(t, got)

	cache := fakeCache{apps: map[string]*App{
		"hermes": appWithVolumes("hermes", []string{"{{dataDir}}/hermes/home"}),
	}}
	got, err = SharedDataToClear(cache, nil, "", "hermes")
	require.NoError(t, err)
	assert.Empty(t, got, "an empty data root must not turn a template into a relative path")
}

func TestSharedDataToClear_ResolvesToCleanAbsolutePaths(t *testing.T) {
	cache := fakeCache{apps: map[string]*App{
		"hermes": appWithVolumes("hermes", []string{"{{dataDir}}/hermes/home/"}),
	}}

	got, err := SharedDataToClear(cache, nil, "/srv/bloud", "hermes")
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("/srv/bloud", "hermes/home")}, got)
}
