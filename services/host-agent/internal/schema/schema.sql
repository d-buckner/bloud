-- Bloud Host Agent Database Schema (SQLite)

CREATE TABLE IF NOT EXISTS apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    catalog_id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
    tailnet_id TEXT DEFAULT '',
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

CREATE TABLE IF NOT EXISTS guests (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS shares (
    id              TEXT PRIMARY KEY,
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    sso_strategy    TEXT NOT NULL DEFAULT 'native-oidc',
    guest_id        TEXT NOT NULL REFERENCES guests(id) ON DELETE CASCADE,
    node_share_link TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL DEFAULT 'active',
    created_at      TEXT DEFAULT (datetime('now')),
    revoked_at      TEXT
);

CREATE TABLE IF NOT EXISTS tailnet_connections (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,
    auth_key    TEXT NOT NULL,
    control_url TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS remote_apps (
    id                   TEXT PRIMARY KEY,
    host_label           TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    app_name             TEXT NOT NULL,
    sso_strategy         TEXT NOT NULL,
    bypass_paths         TEXT NOT NULL DEFAULT '[]',
    tailnet_addr TEXT NOT NULL,
    encrypted_cred       BLOB,
    status               TEXT NOT NULL DEFAULT 'pending_credential',
    created_at           TEXT DEFAULT (datetime('now'))
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
