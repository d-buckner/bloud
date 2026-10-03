// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appasset"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// agentSourceTag is the Hermes agent source release this app is built
	// against. It is deliberately the same release apps/hermes pins, so an
	// instance runs one Hermes agent version whether the user installed the
	// dashboard or this web UI.
	agentSourceTag = "v2026.9.14"

	// agentSourceURL is the upstream source archive for that tag. The webui
	// image does not ship the agent: its entrypoint installs the tree it
	// finds mounted at /home/hermeswebui/.hermes/hermes-agent into the venv
	// it builds on first boot. Without that tree the container still starts,
	// and starts useless: no model auto-detection, no personality routing,
	// no session import, and no agent to talk to.
	agentSourceURL = "https://github.com/NousResearch/hermes-agent/archive/refs/tags/" + agentSourceTag + ".zip"

	// agentSourceSHA256 is the digest the install is verified against, taken
	// from the archive itself (see INTEGRATION.md, "Verified constants").
	agentSourceSHA256 = "c3694a72bf739c76718e31529102f0e4c16f225c2fba0bec3136ba92e127addc"

	// agentSourceMaxBytes raises the installer's 64 MiB default over a
	// source archive that is legitimately larger than that.
	agentSourceMaxBytes = 256 * 1024 * 1024

	// agentSourceDirName is the app-relative directory holding the tree.
	// metadata.yaml mounts it read-only at the path the entrypoint looks in.
	agentSourceDirName = "agent-src"

	// versionMarkerName records which agent source is installed. The archive
	// does not carry it: the configurator writes it after a successful
	// commit, which is what makes the skip on later passes both offline and
	// version-aware.
	versionMarkerName = ".bloud-agent-source"
)

// versionMarker is the marker's whole content. It names the tag and the
// digest, so a bump of either one invalidates an installed tree instead of
// leaving the old one mounted under a new pin.
var versionMarker = "hermes-agent " + agentSourceTag + " " + agentSourceSHA256

// ensureAgentSource makes <dataDir>/agent-src hold the pinned Hermes agent
// source, and reports whether it wrote anything.
//
// The install is skipped outright, with no network, when the marker already
// names this tag. That is what keeps the conformance harness's offline and
// idempotency assertions honest rather than dependent on a 75 MB download,
// and what stops every reconciliation pass from re-fetching the tree.
func (c *Configurator) ensureAgentSource(ctx context.Context, dataDir string) (bool, error) {
	dest := filepath.Join(dataDir, agentSourceDirName)
	if !c.canInstallAssets() {
		// A Deps with no asset cache is the shape a CLI or unit-test context
		// produces: there is no host installer here, so there is nothing to
		// install with. Report no change rather than failing a pass that has
		// no runtime behind it.
		c.logger.Warn("no asset installer available; the Hermes agent source cannot be installed here")
		return false, nil
	}

	changed, err := c.assets.Install(ctx, appasset.Asset{
		Name:     "hermes-agent-source",
		Dest:     dest,
		Source:   appasset.URL(agentSourceURL),
		Kind:     appasset.Zip,
		SHA256:   agentSourceSHA256,
		Strip:    1,
		Sentinel: versionMarkerName,
		SkipIf:   agentSourceInstalled,
		Verify:   verifyAgentSource,
		MaxBytes: agentSourceMaxBytes,
	})
	if err != nil {
		return false, fmt.Errorf("installing the Hermes agent source: %w", err)
	}
	if !changed {
		// The tree is already the pinned one, but the directory holding it
		// still has to be traversable by the container, which reads it as a
		// different uid than the one that wrote it. Cheap enough to assert on
		// every pass rather than trust that the mode was right when it was set.
		return false, c.ensureReadableRoot(dest)
	}
	if err := writeVersionMarker(dest); err != nil {
		return false, fmt.Errorf("recording the installed Hermes agent source: %w", err)
	}
	if err := makeAgentSourceReadable(dest); err != nil {
		return false, fmt.Errorf("making the installed Hermes agent source readable: %w", err)
	}
	c.logger.Info("installed the Hermes agent source", "tag", agentSourceTag, "path", dest)
	return true, nil
}

// ensureReadableRoot makes the agent-source directory itself traversable.
//
// This is not cosmetic. The directory is created with MkdirAll(0755), and
// the process umask applies: on a host with umask 0077 the directory lands
// 0700, owned by the host user. The container mounts it and reads it as
// hermeswebui, uid 1024, which is neither owner nor group, so it cannot
// traverse into its own agent tree. The failure is not an install error and
// not a health-check timeout: it is the app dying at import time with
//
//	PermissionError: [Errno 13] Permission denied:
//	  '/home/hermeswebui/.hermes/hermes-agent/run_agent.py'
//
// which is exactly how this was found, and why the root mode is asserted on
// every pass instead of only after an install.
func (c *Configurator) ensureReadableRoot(dest string) error {
	info, err := os.Stat(dest)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dest, err)
	}
	mode := info.Mode().Perm()
	want := mode | 0o055
	if mode == want {
		return nil
	}
	if err := os.Chmod(dest, want); err != nil {
		return fmt.Errorf("opening %s to the container's uid: %w", dest, err)
	}
	c.logger.Info("opened the agent source directory to the container's uid",
		"path", dest, "from", fmt.Sprintf("%04o", mode), "to", fmt.Sprintf("%04o", want))
	return nil
}

// makeAgentSourceReadable normalizes the freshly installed tree so the
// container can read all of it, not just enter it. The archive's own modes
// are already permissive; this is the belt to ensureReadableRoot's braces,
// and it runs once per install rather than on every pass because the tree
// is tens of thousands of files.
func makeAgentSourceReadable(dest string) error {
	return filepath.Walk(dest, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		mode := info.Mode().Perm()
		want := mode | 0o004
		if info.IsDir() {
			want = mode | 0o055
		}
		if mode == want {
			return nil
		}
		return os.Chmod(path, want)
	})
}

// SeedInstalledAgentSource marks dir as already holding the agent source
// this build pins. It exists for the conformance harness and for tests,
// which need PreStart to take its skip path instead of downloading 75 MB.
// Exported so the seeded value is the real one rather than a copy that can
// drift away from the pin it is supposed to stand for.
func SeedInstalledAgentSource(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeVersionMarker(dir)
}

// agentSourceInstalled reports whether the tree at dest is the one this build
// pins. A missing or unreadable marker means "not installed", which is the
// safe answer: the install is idempotent, and re-running it converges, while
// trusting a stale marker would keep serving a tree the pin no longer
// describes.
func agentSourceInstalled(dest string) bool {
	raw, err := os.ReadFile(filepath.Join(dest, versionMarkerName))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == versionMarker
}

// writeVersionMarker records the installed tag and digest. The marker is
// written by the host into the host-owned source tree, never into the
// container's view of it, because that mount is read-only. Its only reader
// is this package on the same uid, so it is written host-only.
func writeVersionMarker(dest string) error {
	_, err := managedfile.Write(filepath.Join(dest, versionMarkerName),
		[]byte(versionMarker+"\n"), managedfile.ModeHostOnly)
	return err
}

// verifyAgentSource asserts the staged tree is actually a Hermes agent
// checkout before anything is committed. The entrypoint's own discovery
// looks for run_agent.py and pyproject.toml; checking the same markers here
// means a wrong archive (a renamed repo, a changed tag target, a truncated
// download that still hashed right, which cannot happen but should not be
// discoverable only at container start) fails the install instead of
// producing a container that boots with no agent.
func verifyAgentSource(staging string) error {
	for _, marker := range []string{"run_agent.py", "pyproject.toml"} {
		if _, err := os.Stat(filepath.Join(staging, marker)); err != nil {
			return fmt.Errorf("staged Hermes agent source is missing %s: %w", marker, err)
		}
	}
	return nil
}
