// SPDX-License-Identifier: AGPL-3.0-only

// Package dirs is the single definition of the on-disk layout beneath
// BLOUD_DATA_DIR. Every path the host agent computes for an app's private
// state goes through AppDataDir, so the layout has one place to change
// rather than a filepath.Join to hunt down.
//
// The grouping exists so an operator can point one backup rule at one
// directory: $DATA_DIR/apps holds all of app state, and the shared media
// and download trees sit outside it.
//
//	$BLOUD_DATA_DIR/
//	├── bloud.db                 Bloud's own state (SQLite)
//	├── secrets.json
//	├── host-agent-api-token
//	├── traefik/                 generated proxy config
//	├── apps/                    every app's private tree
//	│   └── <app>/               config, data, postgres, ... per metadata.yaml
//	├── media/                   shared: movies, shows, music
//	└── downloads/               shared: the media stack's drop area
//
// The catalog is not part of this tree. metadata.yaml and the configurator
// source live at BLOUD_APPS_DIR, which is a read-only install location,
// and its directory is also named "apps". Only AppDataDir names the
// writable one; the two are never interchangeable.
package dirs

import "path/filepath"

// AppsSubdir is the directory under BLOUD_DATA_DIR holding every app's
// private tree.
const AppsSubdir = "apps"

// AppDataDir returns one app's private data directory: $dataDir/apps/<app>.
//
// Container specs resolve {{appDataDir}} here, and configurators receive
// it as AppState.DataPath, so an app's config, its bundled database, and
// anything else it declares all hang off this path. The app is named by its
// catalog ID, which is also what the io.bloud.app label carries.
func AppDataDir(dataDir, app string) string {
	return filepath.Join(dataDir, AppsSubdir, app)
}
