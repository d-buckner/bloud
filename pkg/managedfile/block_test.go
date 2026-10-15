// SPDX-License-Identifier: AGPL-3.0-only

package managedfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var mk = Marker{Begin: "# BEGIN bloud", End: "# END bloud"}

func TestBlock_CreatesFileWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "configuration.yaml")
	changed, err := Block(path, mk, 0600, func() string {
		return "# BEGIN bloud\nfoo: bar\n# END bloud"
	})
	require.NoError(t, err)
	assert.True(t, changed)
	data, _ := os.ReadFile(path)
	assert.Contains(t, string(data), "foo: bar")
}

func TestBlock_AppendsToExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("user: keep\n"), 0644))
	changed, err := Block(path, mk, 0644, func() string { return "# BEGIN bloud\nx: 1\n# END bloud" })
	require.NoError(t, err)
	assert.True(t, changed)
	data, _ := os.ReadFile(path)
	assert.Contains(t, string(data), "user: keep")
	assert.Contains(t, string(data), "x: 1")
}

func TestBlock_ReplacesExistingRegion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	orig := "user: keep\n# BEGIN bloud\nold: 1\n# END bloud\nafter: yes\n"
	require.NoError(t, os.WriteFile(path, []byte(orig), 0644))
	changed, err := Block(path, mk, 0644, func() string { return "# BEGIN bloud\nnew: 2\n# END bloud" })
	require.NoError(t, err)
	assert.True(t, changed)
	data, _ := os.ReadFile(path)
	assert.NotContains(t, string(data), "old: 1")
	assert.Contains(t, string(data), "new: 2")
	assert.Contains(t, string(data), "user: keep")
	assert.Contains(t, string(data), "after: yes")
}

func TestBlock_UnterminatedRegionReplacedToEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	orig := "user: keep\n# BEGIN bloud\nstale: 1\ntrailing: 2\n"
	require.NoError(t, os.WriteFile(path, []byte(orig), 0644))
	changed, err := Block(path, mk, 0644, func() string { return "# BEGIN bloud\nfresh: 9\n# END bloud" })
	require.NoError(t, err)
	assert.True(t, changed)
	data, _ := os.ReadFile(path)
	assert.NotContains(t, string(data), "stale: 1")
	assert.NotContains(t, string(data), "trailing: 2")
	assert.Contains(t, string(data), "fresh: 9")
}

func TestBlock_NoChangeWhenIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	region := "# BEGIN bloud\nx: 1\n# END bloud\n"
	require.NoError(t, os.WriteFile(path, []byte(region), 0644))
	changed, err := Block(path, mk, 0644, func() string { return "# BEGIN bloud\nx: 1\n# END bloud" })
	require.NoError(t, err)
	assert.False(t, changed, "identical render → changed=false")
}

func TestRemoveBlock_RemovesRegion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	orig := "user: keep\n# BEGIN bloud\nx: 1\n# END bloud\nafter: yes\n"
	require.NoError(t, os.WriteFile(path, []byte(orig), 0644))
	changed, err := RemoveBlock(path, mk)
	require.NoError(t, err)
	assert.True(t, changed)
	data, _ := os.ReadFile(path)
	assert.NotContains(t, string(data), "x: 1")
	assert.Contains(t, string(data), "user: keep")
	assert.Contains(t, string(data), "after: yes")
}

func TestRemoveBlock_NoRegionNoChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(path, []byte("user: keep\n"), 0644))
	changed, err := RemoveBlock(path, mk)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestRemoveBlock_MissingFileNoError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	changed, err := RemoveBlock(path, mk)
	require.NoError(t, err)
	assert.False(t, changed)
}

func TestRender_ChangedReporting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.txt")
	changed, err := Render(path, 0644, func() (string, error) { return "v1", nil })
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = Render(path, 0644, func() (string, error) { return "v1", nil })
	require.NoError(t, err)
	assert.False(t, changed)
	changed, err = Render(path, 0644, func() (string, error) { return "v2", nil })
	require.NoError(t, err)
	assert.True(t, changed)
}
