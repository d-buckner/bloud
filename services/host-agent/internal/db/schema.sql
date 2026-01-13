-- Bloud Host Agent Database Schema (PostgreSQL)

-- Apps installed on this host
CREATE TABLE IF NOT EXISTS apps (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    version TEXT,
    status TEXT NOT NULL DEFAULT 'stopped',  -- 'running', 'stopped', 'error', 'installing'
    port INTEGER,
    is_system BOOLEAN NOT NULL DEFAULT FALSE,    -- true for system apps (postgres, traefik, etc)
    integration_config TEXT,  -- JSON: {"downloadClient": "qbittorrent", "mediaServer": "jellyfin"}
    installed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- NixOS rebuild history
CREATE TABLE IF NOT EXISTS rebuild_history (
    id SERIAL PRIMARY KEY,
    trigger TEXT NOT NULL,        -- 'install_app', 'uninstall_app', 'config_change'
    app_name TEXT,                -- NULL for non-app changes
    status TEXT NOT NULL,         -- 'running', 'success', 'failed'
    log_path TEXT,                -- Path to full rebuild log file
    started_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    completed_at TIMESTAMP
);

-- App catalog cache (synced from git repository)
CREATE TABLE IF NOT EXISTS catalog_cache (
    name TEXT PRIMARY KEY,
    yaml_content TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create indexes for common queries
CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);
CREATE INDEX IF NOT EXISTS idx_rebuild_status ON rebuild_history(status);
CREATE INDEX IF NOT EXISTS idx_rebuild_app ON rebuild_history(app_name);
