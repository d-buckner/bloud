// SPDX-License-Identifier: AGPL-3.0-only

package appasset

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// unpackZip extracts archivePath into stagingDir, stripping the given number
// of leading path components from each entry and rejecting any entry that
// would escape the staging dir (zip-slip guard). File modes are preserved,
// defaulting to 0644 when the archive records 0000.
func unpackZip(archivePath, stagingDir string, strip int) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	cleanStaging := filepath.Clean(stagingDir)
	for _, file := range reader.File {
		name := stripComponents(file.Name, strip)
		if name == "" {
			continue
		}
		destination := filepath.Join(stagingDir, name)
		if !strings.HasPrefix(filepath.Clean(destination), cleanStaging+string(os.PathSeparator)) {
			return fmt.Errorf("archive contains invalid path %q", file.Name)
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		if err := extractEntry(file, destination); err != nil {
			return err
		}
	}
	return nil
}

// extractEntry copies one zip file entry to destination with its mode.
func extractEntry(file *zip.File, destination string) error {
	source, err := file.Open()
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	mode := file.Mode()
	if mode == 0 {
		mode = 0644
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, source)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// stripComponents removes the first n path components from name.
func stripComponents(name string, n int) string {
	if n <= 0 {
		return name
	}
	parts := strings.Split(name, "/")
	if len(parts) <= n {
		return ""
	}
	return strings.Join(parts[n:], "/")
}
