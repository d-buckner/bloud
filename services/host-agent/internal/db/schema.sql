-- Bloud Host Agent Database Schema (SQLite)

CREATE TABLE IF NOT EXISTS apps (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT DEFAULT '',
    status TEXT NOT NULL DEFAULT 'stopped',
    port INTEGER,
    is_system INTEGER NOT NULL DEFAULT 0,
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
