-- Bloud Host Agent Database Schema (PostgreSQL)

-- Users registered in Bloud (credentials stored in Authentik)
CREATE TABLE IF NOT EXISTS users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username TEXT UNIQUE NOT NULL,
    layout JSONB DEFAULT '[]',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

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

-- App catalog cache (synced from git repository)
CREATE TABLE IF NOT EXISTS catalog_cache (
    name TEXT PRIMARY KEY,
    yaml_content TEXT NOT NULL,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

-- Create indexes for common queries
CREATE INDEX IF NOT EXISTS idx_apps_status ON apps(status);
