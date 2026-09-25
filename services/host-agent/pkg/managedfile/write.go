// SPDX-License-Identifier: AGPL-3.0-only

// Package managedfile provides atomic file writes with change detection.
package managedfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// Modes for Bloud-managed files. Every file Bloud writes into an app's data
// directory picks one of these, so the choice is a named decision rather than a
// per-app literal with a rationale comment beside it.
//
// The axis is who has to be able to read the file, not what is in it. Under
// rootless podman an app's container runs as a subordinate uid that is not the
// host uid that wrote the file, so a mode correct for a same-uid reader locks
// the app out of its own configuration.
const (
	// ModeHostOnly is owner read/write. Use it when the readers are the host
	// agent and a container process running as the same host uid (Vaultwarden
	// runs as root inside, which under rootless podman *is* the writing host
	// uid). This is the mode for a file carrying a credential whenever the
	// app can still read it.
	ModeHostOnly os.FileMode = 0o600

	// ModeSharedConfig is world-readable. Use it when the app's own process
	// reads the file and runs as a different uid than the host agent, where
	// 0600 would leave the app unable to read its own configuration at all.
	// A credential in such a file is bounded by the directory it lives in
	// (the app's own data dir), not by the mode; prefer ModeHostOnly where
	// the app's uid allows it.
	ModeSharedConfig os.FileMode = 0o644
)

// Write atomically writes content to path with the given permissions.
// It returns true if the file was created or its content changed, false if the
// existing file already had the exact content. Partial writes never corrupt the
// target because content is first written to a temp file in the same directory
// and then renamed.
func Write(path string, content []byte, mode os.FileMode) (bool, error) {
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, fmt.Errorf("create managed file directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, fmt.Errorf("create managed file temp: %w", err)
	}
	tempPath := temp.Name()
	// Safety-net cleanup: after a successful rename the temp file is already gone
	// (ENOENT), so the Remove error is expected and ignored.
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return false, err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("replace managed file: %w", err)
	}
	return true, nil
}
