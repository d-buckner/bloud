// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

// Package managedfile provides atomic file writes with change detection.
package managedfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return false, err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
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
