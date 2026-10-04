// SPDX-License-Identifier: AGPL-3.0-only

package catalog

import (
	"fmt"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/dirs"
)

// SharedDataToClear returns the shared data trees that a clear-data removal
// of `owner` should take with it, resolved to absolute host paths.
//
// A shared tree -- `$dataDir/hermes/home`, `$dataDir/media` -- is not any
// one app's private state, so the `apps/<name>` removal that backs
// clearData never reaches it. That is correct for a tree the app merely
// consumes: tearing down a front end must not destroy the agent behind it.
// It is wrong for a tree the app is the sole writer of, because the tree
// then survives with no owner at all -- invisible to every later cleanup
// path, and silently picked back up by a reinstall, so "uninstall
// everything and start fresh" is not fresh.
//
// `ownsSharedData` is how an app says which shared trees it owns rather
// than merely mounts. The claim is checked, not trusted: a tree another
// installed app also mounts is dropped from the result, because clearing
// this app would take that app's data with it. A tree that is owned and
// unshared is removed.
//
// `installed` is the set of installed catalog IDs at removal time. The
// owner's own entry is ignored, so the answer is the same whether the store
// has already dropped the row or not.
func SharedDataToClear(cache CacheInterface, installed []string, dataDir, owner string) ([]string, error) {
	if cache == nil || dataDir == "" {
		return nil, nil
	}
	app, err := cache.Get(owner)
	if err != nil || app == nil || len(app.OwnsSharedData) == 0 {
		return nil, nil
	}

	mountedElsewhere, err := mountedSharedPaths(cache, installed, dataDir, owner)
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(app.OwnsSharedData))
	for _, declared := range app.OwnsSharedData {
		path, err := resolveSharedPath(declared, dataDir, owner)
		if err != nil {
			return nil, err
		}
		if mountedElsewhere[path] {
			continue
		}
		out = append(out, path)
	}
	return out, nil
}

// resolveSharedPath renders a declared shared-tree entry against the data
// root. Only `{{dataDir}}` is meaningful here: an entry naming
// `{{appDataDir}}` would be the app's own private directory, which the
// ordinary clearData path already removes, and calling it a shared tree
// would be a declaration that means something else than it says.
func resolveSharedPath(declared, dataDir, owner string) (string, error) {
	trimmed := strings.TrimSpace(declared)
	if trimmed == "" {
		return "", fmt.Errorf("app %q declares an empty ownsSharedData entry", owner)
	}
	if strings.Contains(trimmed, "{{appDataDir}}") {
		return "", fmt.Errorf(
			"app %q ownsSharedData entry %q names {{appDataDir}}: that is the app's own private tree, "+
				"which clearData already removes; ownsSharedData is for shared trees outside apps/",
			owner, declared)
	}
	path := filepath.Clean(strings.ReplaceAll(trimmed, "{{dataDir}}", dataDir))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("app %q ownsSharedData entry %q does not resolve to an absolute path", owner, declared)
	}
	// Guard against an entry that escapes the data root, e.g. "/var" or a
	// crafted "../". A shared tree is by definition inside BLOUD_DATA_DIR.
	rel, err := filepath.Rel(dataDir, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("app %q ownsSharedData entry %q resolves outside the data root %s", owner, declared, dataDir)
	}
	return path, nil
}

// mountedSharedPaths collects every host path another installed app mounts,
// so a shared tree still in use is never cleared as a side effect.
func mountedSharedPaths(cache CacheInterface, installed []string, dataDir, owner string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, id := range installed {
		if id == owner {
			continue
		}
		other, err := cache.Get(id)
		if err != nil || other == nil {
			continue
		}
		ownTree := dirs.AppDataDir(dataDir, id)
		for _, def := range other.ContainerDefs() {
			for _, v := range def.Volumes {
				src := strings.TrimSpace(v.Source)
				if src == "" {
					continue
				}
				rendered := filepath.Clean(strings.ReplaceAll(src, "{{dataDir}}", dataDir))
				// A mount of the other app's own private tree says nothing
				// about the shared trees in play here.
				if rendered == ownTree || strings.HasPrefix(rendered, ownTree+string(filepath.Separator)) {
					continue
				}
				out[rendered] = true
			}
		}
	}
	return out, nil
}
