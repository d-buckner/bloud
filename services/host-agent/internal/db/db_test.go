// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package db

import (
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/schema"
	"github.com/stretchr/testify/require"
)

// TestInitDB_BootsTwicesExercisesProductionUpgradePath covers the
// real entry point: first boot creates and stamps the full ledger,
// a second boot over the same file is a clean no-op.
func TestInitDB_BootsTwiceExercisesProductionUpgradePath(t *testing.T) {
	dir := t.TempDir()

	db, err := InitDB(dir)
	require.NoError(t, err, "first boot")

	var maxVer int
	require.NoError(t, db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&maxVer))
	require.Equal(t, schema.LatestVersion(), maxVer, "first boot must reach the ledger top")

	require.NoError(t, db.Close())

	// Second boot over the existing file: no error, no re-stamping.
	db2, err := InitDB(dir)
	require.NoError(t, err, "second boot")
	defer func() { _ = db2.Close() }()

	var rows int
	require.NoError(t, db2.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&rows))
	require.Equal(t, schema.LatestVersion(), rows, "second boot must not duplicate ledger rows")
}
