// SPDX-License-Identifier: AGPL-3.0-only

package appasset

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ManifestVersionEq returns a SkipIf func that skips the install when the
// manifest at rel (relative to Dest) already reports wantVersion.
func ManifestVersionEq(rel, wantVersion string) func(dest string) bool {
	return func(dest string) bool {
		m, err := readManifest(filepath.Join(dest, rel))
		if err != nil {
			return false
		}
		v, _ := m["version"].(string)
		return v == wantVersion
	}
}

// ManifestFieldEq returns a Verify func that asserts the manifest at rel
// declares field == want before the staged tree is committed.
func ManifestFieldEq(rel, field, want string) func(staging string) error {
	return func(staging string) error {
		m, err := readManifest(filepath.Join(staging, rel))
		if err != nil {
			return fmt.Errorf("manifest %s missing or unreadable: %w", rel, err)
		}
		got, _ := m[field].(string)
		if got != want {
			return fmt.Errorf("manifest %s field %q = %q, want %q", rel, field, got, want)
		}
		return nil
	}
}

// readManifest reads and JSON-decodes a manifest file into a generic map.
func readManifest(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
