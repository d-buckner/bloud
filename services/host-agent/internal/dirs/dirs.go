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
//	├── downloads/               shared: the media stack's drop area
//	└── shared-trees/            shared: two named apps mount one directory
//	    └── hermes/inbox/        hermes-webui's uploads, opened by the agent
//
// shared-trees/ sits outside apps/ for the same mount reason as media/ and
// downloads/ and is not the same kind of thing as either. Those two are a
// library: large, replaceable, and deliberately left out of a backup. A
// shared-trees/ directory is app state that two apps happen to agree on a path
// for, and it belongs inside one. The Hermes agent cannot open an upload unless
// the directory hermes-webui writes it to is one the agent can also see, which
// is the only reason this root exists.
//
// Nothing in this package creates one. The orchestrator makes the mount source at
// 0755 (orchestrator/execution.go, ensureMountDirs) and the apps that mount it set
// the mode in their own PreStart, because only they know the mode both containers
// need.
//
// A shared tree is not removed when an app is. Uninstall reaches
// AppDataDir(<app>) and the orphan sweep under apps/, so the tree outlives both of
// its apps and a "clear data" uninstall leaves the uploads in place. That is a
// retention promise as much as a gap: what is in there is still someone's files.
//
// The name is deliberately not shared/. backup.sh already uses "shared" for
// media/ and downloads/ in its --include-shared flag, and a directory called
// shared/ that the flag does not mean would read as a bug in the flag.
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
