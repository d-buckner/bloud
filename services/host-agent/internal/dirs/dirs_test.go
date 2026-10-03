// SPDX-License-Identifier: AGPL-3.0-only

package dirs

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAppDataDir_groupsUnderAppsSubdir(t *testing.T) {
	got := AppDataDir("/var/lib/bloud", "jellyfin")

	assert.Equal(t, "/var/lib/bloud/apps/jellyfin", got)
	assert.Equal(t, filepath.Join("/var/lib/bloud", AppsSubdir, "jellyfin"), got)
}

// TestAppDataDir_isNotTheCatalog pins the distinction the layout depends on.
// BLOUD_APPS_DIR is the read-only catalog and is also named "apps"; an app's
// private tree sits under a sibling "apps" of the data root instead. A change
// that made the two resolve to the same place would let orphaned-data cleanup
// delete installed catalog source.
func TestAppDataDir_isNotTheCatalog(t *testing.T) {
	dataDir := "/var/lib/bloud"
	catalogDir := "/usr/share/bloud/apps"

	appData := AppDataDir(dataDir, "immich")

	assert.NotEqual(t, filepath.Join(catalogDir, "immich"), appData)
	assert.NotContains(t, appData, catalogDir)
}

// TestAppDataDir_distinctPerApp guards the shared-tree namespace. Two apps must
// never resolve to the same directory, and neither may land on the shared
// media or downloads trees that sit one level above apps/.
func TestAppDataDir_distinctPerApp(t *testing.T) {
	dataDir := "/var/lib/bloud"

	media := AppDataDir(dataDir, "media")
	downloads := AppDataDir(dataDir, "downloads")

	assert.Equal(t, filepath.Join(dataDir, "apps", "media"), media)
	assert.NotEqual(t, filepath.Join(dataDir, "media"), media,
		"an app named 'media' must not resolve onto the shared media tree")
	assert.NotEqual(t, filepath.Join(dataDir, "downloads"), downloads,
		"an app named 'downloads' must not resolve onto the shared downloads tree")
	assert.NotEqual(t, media, downloads)
}
