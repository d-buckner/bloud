-- SPDX-License-Identifier: AGPL-3.0-only
-- Bloud Host Agent Database Schema (SQLite)

CREATE TABLE IF NOT EXISTS apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    last_error TEXT NOT NULL DEFAULT '',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
    sso_strategy TEXT NOT NULL DEFAULT '',
    integration_config TEXT DEFAULT '{}',
    installed_at TEXT DEFAULT (datetime('now')),
    updated_at TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);

CREATE TABLE IF NOT EXISTS user_preferences (
    username TEXT PRIMARY KEY,
    layout TEXT DEFAULT '[]',
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS graph_nodes (
    id            TEXT PRIMARY KEY,
    target_status TEXT NOT NULL DEFAULT 'INITIALIZING',
    actual_status TEXT NOT NULL DEFAULT 'INITIALIZING',
    error         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS graph_edges (
    dependent_id  TEXT NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    dependency_id TEXT NOT NULL REFERENCES graph_nodes(id) ON DELETE CASCADE,
    PRIMARY KEY (dependent_id, dependency_id)
);

CREATE TABLE IF NOT EXISTS user_app_positions (
    username     TEXT    NOT NULL REFERENCES user_preferences(username) ON DELETE CASCADE,
    element_id   TEXT    NOT NULL,
    element_type TEXT    NOT NULL,
    x            INTEGER,
    y            INTEGER,
    w            INTEGER NOT NULL DEFAULT 1,
    h            INTEGER NOT NULL DEFAULT 1,
    PRIMARY KEY (username, element_id)
);

-- Instance-level scalar settings, one row per key. This is where a single
-- admin-editable value goes instead of each one getting its own table; the
-- public address is the first entry. See store/settings.go.
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    username   TEXT NOT NULL,
    role       TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_username ON sessions(username);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions(expires_at);

-- Durable lifecycle operation state: current-or-last drive per app.
-- Graph state controls convergence; this row explains user-intent
-- outcome (which phase, retryability, cause) and crash visibility.
-- See docs/plans/operation-state-design.md.
CREATE TABLE IF NOT EXISTS operations (
    app_name   TEXT PRIMARY KEY,
    id         TEXT NOT NULL,
    type       TEXT NOT NULL,
    phase      TEXT NOT NULL,
    status     TEXT NOT NULL,
    retryable  INTEGER NOT NULL DEFAULT 1,
    cause      TEXT NOT NULL DEFAULT '',
    started_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Operator-declared pointers to things Bloud does not run: a launcher tile,
-- a remote install of a catalog app, or an off-host provider. Credentials are
-- deliberately absent here: they live in the secrets manager under an
-- external/<id> scope, never in this table. See docs/plans/external-apps.md.
CREATE TABLE IF NOT EXISTS external_apps (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT '',
    name        TEXT NOT NULL,
    url         TEXT NOT NULL,
    icon        TEXT NOT NULL DEFAULT '',
    values_json TEXT NOT NULL DEFAULT '{}',
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
