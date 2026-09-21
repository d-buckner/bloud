// SPDX-License-Identifier: AGPL-3.0-only

// Package schema holds the single source of truth for the host-agent
// SQLite schema plus the versioned migration ledger that upgrades
// databases to it. Both the production database (db.InitDB) and the
// test databases apply the same embedded schema.sql through
// Migrate, so the two can never drift apart.
package schema

import _ "embed"

//go:embed schema.sql
var SQL string

// Baseline DDL (schema.sql) corresponds to the top of the ledger:
// see Migrations and LatestVersion in migrations.go.
