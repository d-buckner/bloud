// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"database/sql"
	"testing"
)

// createLegacyOperations builds the pre-13 `operations` table: keyed by
// app alone, one row per app.
func createLegacyOperations(t *testing.T, db *sql.DB) {
	t.Helper()
	const ddl = `CREATE TABLE operations (
		app_name   TEXT PRIMARY KEY,
		id         TEXT NOT NULL,
		type       TEXT NOT NULL,
		phase      TEXT NOT NULL,
		status     TEXT NOT NULL,
		retryable  INTEGER NOT NULL DEFAULT 1,
		cause      TEXT NOT NULL DEFAULT '',
		started_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatalf("create legacy operations: %v", err)
	}
}

func insertLegacyOperation(t *testing.T, db *sql.DB, app, id, phase, status string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO operations (app_name, id, type, phase, status) VALUES (?, ?, 'install', ?, ?)`,
		app, id, phase, status)
	if err != nil {
		t.Fatalf("insert legacy operation for %s: %v", app, err)
	}
}

// The re-key is a table rebuild, so the test that matters is that an
// upgraded database ends up with the same shape a fresh one gets and
// keeps every row it had.
func TestMigrate_KeysOperationsByNode(t *testing.T) {
	db := setupMigrated(t)
	dropLedgerVersionsAbove(t, db, 12)

	if _, err := db.Exec(`DROP TABLE operations`); err != nil {
		t.Fatalf("drop node-keyed operations: %v", err)
	}
	createLegacyOperations(t, db)
	insertLegacyOperation(t, db, "immich", "op-1", "health", "failed")
	insertLegacyOperation(t, db, "jellyfin", "op-2", "complete", "complete")

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if got := ledgerMaxVersion(t, db); got != 13 {
		t.Fatalf("ledger top = %d, want 13", got)
	}

	// The composite key is what the store's ON CONFLICT clause names. A
	// rebuild that kept the old primary key would still copy the rows, so
	// the key is asserted on its own rather than inferred from the data.
	if _, err := db.Exec(`INSERT INTO operations (app_name, node, id, type, phase, status)
		VALUES ('immich', 'apps-immich-ml', 'op-3', 'reconcile', 'topology', 'running')`); err != nil {
		t.Fatalf("second node of one app was rejected, so the key is still app-only: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO operations (app_name, node, id, type, phase, status)
		VALUES ('immich', 'apps-immich-ml', 'op-4', 'reconcile', 'topology', 'running')`); err == nil {
		t.Fatal("(app_name, node) is not unique: the rebuild did not carry the key")
	}

	// Each old row survived, and its node names the app: the app's own
	// node carries the app's catalog id, so the row means the same thing.
	var node string
	if err := db.QueryRow(`SELECT node FROM operations WHERE app_name = 'immich' AND id = 'op-1'`).Scan(&node); err != nil {
		t.Fatalf("carried-forward immich row is missing: %v", err)
	}
	if node != "immich" {
		t.Errorf("carried-forward node = %q, want %q", node, "immich")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operations`).Scan(&n); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if n != 3 {
		t.Errorf("row count after re-key = %d, want 3 (two carried forward, one inserted)", n)
	}
}

// A fresh database is created in the final shape by schema.sql, so the
// re-key must be a no-op there rather than a failure or a data rewrite.
func TestMigrate_OperationsReKeyIsANoOpOnTheCurrentShape(t *testing.T) {
	db := setupMigrated(t)
	insertLegacyOperation := func() {
		t.Helper()
		_, err := db.Exec(`INSERT INTO operations (app_name, node, id, type, phase, status)
			VALUES ('jellyfin', 'jellyfin', 'op-1', 'install', 'complete', 'complete')`)
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	insertLegacyOperation()
	dropLedgerVersionsAbove(t, db, 12)

	if err := Migrate(db); err != nil {
		t.Fatalf("re-running the re-key on the current shape: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM operations WHERE id = 'op-1'`).Scan(&n); err != nil {
		t.Fatalf("count operations: %v", err)
	}
	if n != 1 {
		t.Errorf("row count after a no-op re-key = %d, want 1", n)
	}
}
