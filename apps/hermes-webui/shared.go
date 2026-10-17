// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// This file holds the trees this app shares with Hermes, and the mount points the
// web UI's workspace and media allowlist point at. The constants are pinned to
// apps/hermes by the top-level tests, because a drift on either side means the
// path the agent writes and the path the web UI serves are different paths again.
const (
	// sharedInboxRel is the attachment inbox's path relative to the shared Bloud
	// data root. It is the same tree Hermes mounts, and the two declarations are
	// pinned together by TestSharedInboxMountsAgree: the whole point of the inbox is
	// that both containers address it identically, so a drift here from
	// metadata.yaml or from the Hermes side is the original bug back again.
	sharedInboxRel = "shared-trees/hermes/inbox"

	// sharedInboxMode is the inbox's directory mode: world-writable plus the sticky
	// bit. The world bits are what reach across, because this app's runtime user
	// (uid 1024) and Hermes' (uid 10000) are unrelated subordinate uids with no shared
	// group, so an upload is only readable by the agent if its other-read bit is set.
	// The sticky bit keeps that from meaning anyone may delete anyone's upload: this
	// app ships /api/file/delete, and the agent has shell access, so without it every
	// signed-in user and the agent itself could unlink someone else's file. With it an
	// owner still removes its own uploads, so the app's own delete API is unaffected.
	//
	// The literal is duplicated in apps/hermes and each package's own test asserts
	// this exact value, so a drift in either half fails a test.
	//
	// Written as os.ModeSticky rather than os.FileMode(0o1777): FileMode's sticky flag
	// is its own high bit, not the 0o1000 bit of a stat mode, so the octal literal
	// silently produces a plain 0777.
	sharedInboxMode = os.FileMode(0o777) | os.ModeSticky

	// sharedWorkspaceRel and sharedWorkspacePath are the tree Hermes pins its working
	// directory to, shared so the web UI can browse, edit, and serve what the agent
	// writes. The path is the web UI's own convention, so an existing install's stored
	// workspace stays valid and new chats keep starting; HERMES_WEBUI_DEFAULT_WORKSPACE
	// names it for a fresh install.
	sharedWorkspaceRel  = "shared-trees/hermes/workspace"
	sharedWorkspacePath = "/workspace"

	// sharedCacheRel and sharedCachePath are the agent's media cache, mounted here
	// read-only so the MEDIA: tags the agent's tools emit resolve to bytes this app can
	// snapshot and serve. Hermes mounts it read-write; this app only reads it.
	sharedCacheRel  = "shared-trees/hermes/cache"
	sharedCachePath = "/opt/data/cache"

	// sharedDirMode is the workspace mode: world-writable, no sticky bit. The world
	// bits are what reach the agent's uid across the rootless podman boundary, and the
	// sticky bit is deliberately absent so the agent can rename and remove files this
	// app created in the same tree, and this app the reverse.
	sharedDirMode = os.FileMode(0o777)
)

// ensureDirMode creates dir and leaves it at mode.
//
// The host agent creates and owns every shared mount source, so a chmod against one
// always succeeds and needs none of the foreign-uid tolerance prepareDataDir
// describes. managedfile.EnsureWritable also cannot be used here: it compares only
// .Perm(), so it returns early on a directory that is already 0777 and never adds the
// sticky bit, which would leave fresh inbox installs sticky and upgraded installs not.
//
// The sticky bit is compared separately because FileMode.Perm() masks it away, so a
// 1777 directory would otherwise look already correct to a 1777 target.
//
// Idempotent and a no-op once the mode is right, so the periodic resync stays a
// read-only diff (invariant 2).
func ensureDirMode(dir string, mode os.FileMode) error {
	if err := os.MkdirAll(dir, mode); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != mode.Perm() || info.Mode()&os.ModeSticky != mode&os.ModeSticky {
		return os.Chmod(dir, mode)
	}
	return nil
}

// prepareInbox creates the shared attachment inbox and makes it writable by this
// app's runtime user.
//
// The orchestrator creates every mount source at 0755 owned by the host agent
// (orchestrator/execution.go, ensureMountDirs). That is readable by Hermes and
// unwritable here: this app's runtime uid is a subordinate uid, not the owner and
// not a group member, so only the world-write bit lets it create the per-session
// directory an upload needs. Without this the mount exists and every upload fails
// with EACCES instead of the ENOENT it had before the mount.
//
// A missing shared root is a WARN, not a failure, for the same reason the client
// password is: offline and CLI contexts have none, and a pass that still writes the
// app's real config beats an ERROR node. The warning is what keeps a genuine
// misconfiguration from being invisible.
func (c *Configurator) prepareInbox(state *configurator.AppState) error {
	if state.BloudDataPath == "" {
		c.logger.Warn("no shared data root; the attachment inbox is not prepared")
		return nil
	}
	if err := ensureDirMode(filepath.Join(state.BloudDataPath, sharedInboxRel), sharedInboxMode); err != nil {
		return fmt.Errorf("%s: prepare attachment inbox: %w", appName, err)
	}
	return nil
}

// prepareSharedDirs creates and widens the shared workspace this app writes to.
//
// The web UI resolves HERMES_WEBUI_DEFAULT_WORKSPACE only when the directory exists
// and is writable: a 0755 mount source owned by the host agent fails that check, and
// the app then silently falls back to a workspace under its own home and browses a
// tree the agent never writes. Raising the mode here is what makes the mount
// actually used.
//
// The media cache is not prepared here. This app only reads it, Hermes creates it,
// and a mode this app sets would only be a second writer's opinion about a tree it
// does not write.
//
// A missing shared root is a WARN for the same reason as the inbox: the app still
// runs, on its own fallback workspace, and the warning names why.
func (c *Configurator) prepareSharedDirs(state *configurator.AppState) error {
	if state.BloudDataPath == "" {
		c.logger.Warn("no shared data root; the shared workspace is not prepared")
		return nil
	}
	if err := ensureDirMode(filepath.Join(state.BloudDataPath, sharedWorkspaceRel), sharedDirMode); err != nil {
		return fmt.Errorf("%s: prepare shared workspace: %w", appName, err)
	}
	return nil
}
