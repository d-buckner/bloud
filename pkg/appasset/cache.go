// SPDX-License-Identifier: AGPL-3.0-only

package appasset

import (
	"os"
	"path/filepath"
)

// cachedPath returns the cache file path for a digest and whether it exists.
// An empty CacheDir means caching is disabled.
func (in Installer) cachedPath(sha string) (string, bool) {
	if in.CacheDir == "" || sha == "" {
		return "", false
	}
	p := filepath.Join(in.CacheDir, sha)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// storeCache copies a verified payload into the content cache. Best-effort:
// a cache write failure never fails the install.
func (in Installer) storeCache(sha, tempPath string) {
	if in.CacheDir == "" || sha == "" {
		return
	}
	if err := os.MkdirAll(in.CacheDir, 0755); err != nil {
		return
	}
	data, err := os.ReadFile(tempPath)
	if err != nil {
		return
	}
	tmp := filepath.Join(in.CacheDir, sha+".tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(in.CacheDir, sha))
}

// deleteCache removes a poisoned cache entry (digest mismatch).
func (in Installer) deleteCache(sha string) {
	if in.CacheDir == "" || sha == "" {
		return
	}
	_ = os.Remove(filepath.Join(in.CacheDir, sha))
	_ = os.Remove(filepath.Join(in.CacheDir, sha+".tmp"))
}
