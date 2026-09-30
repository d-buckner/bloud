// SPDX-License-Identifier: AGPL-3.0-only

package schema

import (
	"testing"

	_ "modernc.org/sqlite"
)

// The collapse carries the primary host's scheme and hostname forward as one
// origin, and drops the table the code no longer reads. A deployment that had
// configured extra domains loses those aliases by design: the new model has no
// way to express a second address.
func TestMigrate_CollapsesHostsIntoPublicURLSetting(t *testing.T) {
	db := openRawDB(t)

	if _, err := db.Exec(`CREATE TABLE hosts (
		hostname TEXT PRIMARY KEY,
		is_primary INTEGER NOT NULL DEFAULT 0,
		scheme TEXT NOT NULL DEFAULT '',
		created_at TEXT DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create legacy hosts: %v", err)
	}
	for _, row := range []struct {
		host, scheme string
		primary      int
	}{
		{"bloud.local", "", 0},
		{"home.thebloud.org", "https", 1},
		{"old.example.com", "", 0},
	} {
		if _, err := db.Exec(
			`INSERT INTO hosts (hostname, is_primary, scheme) VALUES (?, ?, ?)`,
			row.host, row.primary, row.scheme,
		); err != nil {
			t.Fatalf("insert legacy host: %v", err)
		}
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'public_url'`).Scan(&value); err != nil {
		t.Fatalf("read public_url setting: %v", err)
	}
	if value != "https://home.thebloud.org" {
		t.Errorf("public_url = %q, want the primary host's origin", value)
	}

	if ok, _ := tableExists(db, "hosts"); ok {
		t.Error("the retired hosts table should be dropped")
	}
}

// An empty scheme on the primary row means http, not "no statement": the
// stored origin has to be a complete URL.
func TestMigrate_CollapseDefaultsToHTTPScheme(t *testing.T) {
	db := openRawDB(t)

	if _, err := db.Exec(`CREATE TABLE hosts (
		hostname TEXT PRIMARY KEY,
		is_primary INTEGER NOT NULL DEFAULT 0,
		scheme TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create legacy hosts: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO hosts (hostname, is_primary, scheme) VALUES ('plain.example.com', 1, '')`,
	); err != nil {
		t.Fatalf("insert legacy host: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'public_url'`).Scan(&value); err != nil {
		t.Fatalf("read public_url setting: %v", err)
	}
	if value != "http://plain.example.com" {
		t.Errorf("public_url = %q, want http://plain.example.com", value)
	}
}

// No custom host ever configured means no public_url setting at all, so
// resolution falls through to the env knobs and then the default rather than
// being pinned to an empty string.
func TestMigrate_CollapseWithNoCustomHosts(t *testing.T) {
	db := openRawDB(t)

	if _, err := db.Exec(`CREATE TABLE hosts (
		hostname TEXT PRIMARY KEY,
		is_primary INTEGER NOT NULL DEFAULT 0,
		scheme TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create legacy hosts: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'public_url'`).Scan(&count); err != nil {
		t.Fatalf("count public_url setting: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no public_url setting, got %d rows", count)
	}
	if ok, _ := tableExists(db, "hosts"); ok {
		t.Error("the retired hosts table should be dropped")
	}
}

// Re-running the ledger must not overwrite a value the operator set after the
// first pass, and must not fail on the already-dropped table.
func TestMigrate_CollapseIsIdempotent(t *testing.T) {
	db := openRawDB(t)

	if _, err := db.Exec(`CREATE TABLE hosts (
		hostname TEXT PRIMARY KEY,
		is_primary INTEGER NOT NULL DEFAULT 0,
		scheme TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		t.Fatalf("create legacy hosts: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO hosts (hostname, is_primary, scheme) VALUES ('first.example.com', 1, 'https')`,
	); err != nil {
		t.Fatalf("insert legacy host: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}

	// The operator moves the address after the upgrade.
	if _, err := db.Exec(
		`UPDATE settings SET value = 'https://moved.example.com' WHERE key = 'public_url'`,
	); err != nil {
		t.Fatalf("update setting: %v", err)
	}

	// A second pass over the whole ledger must leave the moved value alone.
	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop ledger: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	var value string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key = 'public_url'`).Scan(&value); err != nil {
		t.Fatalf("read public_url setting: %v", err)
	}
	if value != "https://moved.example.com" {
		t.Errorf("public_url = %q; a re-run must not resurrect the old host", value)
	}
}
