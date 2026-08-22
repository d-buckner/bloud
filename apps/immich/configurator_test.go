// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package immich

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestEnsureMountMarkers_CreatesAllFoldersAndMarkers(t *testing.T) {
	dataPath := t.TempDir()

	require.NoError(t, ensureMountMarkers(dataPath, quietLogger()))

	for _, folder := range mountFolders {
		marker := filepath.Join(dataPath, "upload", folder, ".immich")
		info, err := os.Stat(marker)
		require.NoError(t, err, "marker for %s must exist", folder)
		assert.False(t, info.IsDir(), "marker for %s must be a file", folder)
		content, err := os.ReadFile(marker)
		require.NoError(t, err)
		assert.NotEmpty(t, content, "marker for %s must carry content", folder)
	}
}

func TestEnsureMountMarkers_IsIdempotent(t *testing.T) {
	dataPath := t.TempDir()
	logger := quietLogger()

	require.NoError(t, ensureMountMarkers(dataPath, logger))

	// First markers' content must survive a second run (the server may have
	// recorded the check as passed; only existence matters to it).
	first := map[string][]byte{}
	for _, folder := range mountFolders {
		content, err := os.ReadFile(filepath.Join(dataPath, "upload", folder, ".immich"))
		require.NoError(t, err)
		first[folder] = content
	}

	require.NoError(t, ensureMountMarkers(dataPath, logger))

	for folder, before := range first {
		after, err := os.ReadFile(filepath.Join(dataPath, "upload", folder, ".immich"))
		require.NoError(t, err)
		assert.Equal(t, before, after, "marker for %s must not be rewritten", folder)
	}
}
