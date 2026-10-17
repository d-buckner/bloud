// SPDX-License-Identifier: AGPL-3.0-only

package hermes

import (
	"fmt"
	"os"
	"path/filepath"

	"codeberg.org/d-buckner/bloud/pkg/configurator"
)

// This file holds the trees Hermes shares with hermes-webui, and the pin that
// makes the agent write into one of them. Everything here exists so a file the
// agent produces is a file the web UI can serve; see apps/hermes/INTEGRATION.md.
//
// Hermes confines its file tools to HERMES_WRITE_SAFE_ROOT. The image's value
// covers /opt/data alone, and metadata.yaml widens it to include the shared
// workspace at /workspace.
const (
	// sharedInboxRel is the attachment inbox's path relative to the shared Bloud
	// data root, mounted at /bloud-hermes/inbox in this container and in
	// hermes-webui. Hermes is the reader here: the web UI writes an upload and
	// puts this path into the message, and the agent opens it with its own file
	// tools. The three declarations (this constant, both metadata.yaml files) are
	// pinned together by TestSharedInboxMountsAgree.
	sharedInboxRel = "shared-trees/hermes/inbox"

	// sharedInboxMode is 1777: world-writable, plus the sticky bit.
	//
	// The world bits are what reach across. The two containers run as unrelated
	// subordinate uids with no shared group, so a file hermes-webui writes is only
	// readable by Hermes if its other-read bit is set. The sticky bit is what stops
	// that from meaning "anyone may delete anyone's upload": the agent has shell
	// access and every signed-in web UI user reaches this tree through the app's own
	// file API, and without it either can unlink a file they do not own. With it an
	// owner still deletes and renames its own uploads, so the web UI's delete API
	// keeps working.
	//
	// The literal is duplicated in apps/hermes-webui and each package's own test
	// asserts this exact value, so a drift in either half fails a test.
	//
	// Written as os.ModeSticky rather than os.FileMode(0o1777): FileMode's sticky
	// flag is its own high bit, not the 0o1000 bit of a stat mode, so the octal
	// literal silently produces a plain 0777.
	sharedInboxMode = os.FileMode(0o777) | os.ModeSticky

	// sharedWorkspaceRel is the agent's working directory, relative to the shared
	// Bloud data root, and sharedWorkspacePath is where both containers mount it.
	//
	// The path is the web UI's own convention, and that is load-bearing on an
	// existing install: the app trusts a path under its boot default and already has
	// /workspace in its stored last-workspace, so mounting here keeps every earlier
	// session valid. A path elsewhere would leave new chats failing to start with
	// "Path is outside the user home directory, not in the saved workspace list".
	//
	// The agent's file tools are confined to HERMES_WRITE_SAFE_ROOT, which the image
	// sets to /opt/data alone. metadata.yaml widens it to /opt/data:/workspace, and
	// that widening is what lets the agent write its own workspace. The old
	// {{appDataDir}}/data/workspace directory is no longer used.
	//
	// The pin itself is TERMINAL_CWD in metadata.yaml, not a terminal.cwd config key:
	// config.yaml carries only what an operator set, and the config bridge runs last,
	// so an operator's own terminal.cwd still wins.
	sharedWorkspaceRel  = "shared-trees/hermes/workspace"
	sharedWorkspacePath = "/workspace"

	// sharedCacheRel is the agent's media cache, relative to the shared Bloud data
	// root, and sharedCachePath is where both containers mount it.
	//
	// Tool-generated media (browser screenshots, MCP image blocks, TTS) is written
	// under $HERMES_HOME/cache/... and delivered as a MEDIA: tag naming that absolute
	// location. The web UI snapshots and serves such a tag only if the bytes exist in
	// its own container at the same path, so this tree carries them there. Hermes
	// mounts it read-write; hermes-webui mounts it read-only, because the cache is
	// the agent's to write and the web UI only reads it.
	sharedCacheRel  = "shared-trees/hermes/cache"
	sharedCachePath = "/opt/data/cache"

	// sharedBrowserTmpRel and sharedBrowserTmpPath are the browser tools' scratch
	// directory, inside the shared cache tree so it is served with it.
	//
	// The browser harness (browser-use / browser-harness, wrapped by Hermes'
	// browser_exec) writes its default screenshot to BH_TMP_DIR, and it defaults to
	// $HOME/.config/browser-harness/tmp: a path outside every shared tree, so
	// /api/media answers 403 for the screenshot the agent just took and put in its
	// reply. metadata.yaml points BH_TMP_DIR here, so the default screenshot lands in
	// the tree the web UI already serves. AGENT_BROWSER_SCREENSHOT_DIR points at the
	// same directory, for the bundled agent-browser skill.
	//
	// It is created at 0777 before the container starts. The harness chmods its tmp
	// directory to 0700 only when it creates the directory itself, so pre-creating it
	// is what keeps the web UI able to read a screenshot.
	sharedBrowserTmpRel  = "shared-trees/hermes/cache/browser-harness"
	sharedBrowserTmpPath = "/opt/data/cache/browser-harness"

	// sharedDirMode is the workspace and media cache mode: world-writable, no sticky
	// bit. The world bits are what reach across the rootless podman uid boundary, the
	// same contract the inbox uses. The sticky bit is deliberately absent: the agent
	// and the web UI edit the same workspace files, so the agent must be able to
	// rename and remove what the web UI created, and the web UI the reverse.
	sharedDirMode = os.FileMode(0o777)
)

// ensureDirMode creates dir and leaves it at mode.
//
// The host agent creates and owns every shared mount source, so a chmod against
// one always succeeds and needs none of the foreign-uid tolerance
// managedfile.EnsureWritable exists to provide. managedfile.EnsureWritable also
// cannot be used here: it compares only .Perm(), so it returns early on a
// directory that is already 0777 and never adds the sticky bit, which would leave
// fresh inbox installs sticky and upgraded installs not.
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

// prepareInbox creates the shared attachment inbox and widens it to the mode both
// containers can use.
//
// The orchestrator creates every mount source at 0755 owned by the host agent
// (orchestrator/execution.go, ensureMountDirs). Hermes can read that but
// hermes-webui cannot write it, and an upload that cannot be written is the bug
// this tree exists to fix.
//
// A missing shared root is a WARN, not a failure. Offline and CLI contexts have
// none, and Hermes is the reader of this tree rather than the writer: parking the
// node in ERROR over a directory only uploads need would take the dashboard, the
// gateway, and every MCP wiring down with it. Saying so out loud is what keeps a
// real misconfiguration from being invisible.
func (c *Configurator) prepareInbox(state *configurator.AppState) error {
	if state == nil || state.BloudDataPath == "" {
		c.logger.Warn("no shared data root; the attachment inbox is not prepared")
		return nil
	}
	if err := ensureDirMode(filepath.Join(state.BloudDataPath, sharedInboxRel), sharedInboxMode); err != nil {
		return fmt.Errorf("%s: prepare attachment inbox: %w", appName, err)
	}
	return nil
}

// prepareSharedDirs creates and widens the two trees Hermes shares with
// hermes-webui: the workspace both write, and the media cache the web UI reads.
//
// A missing shared root is a WARN for the same reason the inbox treats it that
// way: the agent still runs, and a node parked in ERROR over directories it can
// live without is worse than an app whose config was written.
func (c *Configurator) prepareSharedDirs(state *configurator.AppState) error {
	if state == nil || state.BloudDataPath == "" {
		c.logger.Warn("no shared data root; the shared workspace and media cache are not prepared")
		return nil
	}
	dirs := []struct {
		name string
		rel  string
	}{
		{"workspace", sharedWorkspaceRel},
		{"media cache", sharedCacheRel},
		{"browser scratch", sharedBrowserTmpRel},
	}
	for _, d := range dirs {
		if err := ensureDirMode(filepath.Join(state.BloudDataPath, d.rel), sharedDirMode); err != nil {
			return fmt.Errorf("%s: prepare shared %s: %w", appName, d.name, err)
		}
	}
	return nil
}
