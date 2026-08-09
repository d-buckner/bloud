# Bloud Host Agent

> **Current implementation reference:** This README describes the host-agent development
> workflow. [SPEC.md](../../SPEC.md) is the authoritative first-release plan.

Go service that manages app installation, system monitoring, and provides a web UI for the Bloud home server platform.

## Architecture

- **Backend**: Go HTTP server with SQLite database
- **Frontend**: SvelteKit with SSR/SSG (embedded in Go binary)
- **Deployment**: Portable binary managed by systemd; standalone during development

## Prerequisites

- **Go 1.21+** - [Install Go](https://go.dev/doc/install)
- **Node.js 18+** - [Install Node](https://nodejs.org/)
- **npm** or **pnpm**

## Quick Start (Local Development on macOS)

### 1. Install Dependencies

```bash
# From project root
npm install

# Install Go dependencies (will be downloaded on first build)
cd services/host-agent
go mod download
```

### 2. Run Development Servers

**Option A: Run them separately (recommended for active development)**

Terminal 1 — Go Backend:
```bash
cd services/host-agent
npm run dev
```

Terminal 2 — Frontend:
```bash
cd services/host-agent/web
npm run dev
```

The frontend dev server (port 5173) proxies API requests to the Go backend (port 8080).

**Option B: Run Go with embedded frontend**

```bash
cd services/host-agent/web
npm run build
```

Then:
```bash
cd services/host-agent
npm run dev
```

This builds the SvelteKit app into `web/build/` and the Go binary serves it at `/`.

### 3. Access the Application

- **Frontend (dev)**: http://localhost:5173
- **Backend API**: http://localhost:8080/api/health
- **Backend direct**: http://localhost:8080

## Development Workflow

### Frontend

The SvelteKit app supports SSR/SSG and hot reload during development. Build output
lands in `web/build/` for embedding into the Go binary.

```bash
cd services/host-agent/web
npm run dev    # Dev server with hot reload
npm run build  # Production build → web/build/
```

### Backend

The Go binary serves the REST API and optionally embeds the built frontend at `/`.

```bash
cd services/host-agent

# Run with hot reload (requires air: go install github.com/cosmtrek/air@latest)
air

# Run directly
npm run dev

# Build production binary (embeds web/build/ if present)
go build -o bin/host-agent ./cmd/host-agent
```

### Environment Variables

```bash
export BLOUD_PORT=8080                          # HTTP port (default: 8080)
export BLOUD_DATA_DIR=$HOME/.local/share/bloud  # Data directory
export BLOUD_RUNTIME=portable                   # Only supported value
```

### Database

SQLite database is automatically created at:
```
$BLOUD_DATA_DIR/state/bloud.db
```

Schema is initialized on first run from `internal/db/schema.sql`.

## Building for Production

### 1. Build Frontend

```bash
cd services/host-agent/web
npm run build
# Creates: web/build/
```

### 2. Build Go Binary (with embedded frontend)

```bash
cd services/host-agent
go build -o bin/host-agent ./cmd/host-agent
```

The Go binary will embed the `web/build/` directory and serve it at `/`.

### 3. Run Production Binary

```bash
./bin/host-agent
```

The portable runtime is the default. It requires an accessible Podman API socket:

```bash
export BLOUD_RUNTIME=portable
export BLOUD_PODMAN_SOCKET="${XDG_RUNTIME_DIR}/podman/podman.sock"
export BLOUD_DATA_DIR="${HOME}/.local/share/bloud"
export BLOUD_APPS_DIR="$(pwd)/../../apps"
./bin/host-agent
```

The portable runtime owns managed application containers and networks, creating
and starting them directly through the Podman API. It refuses to remove or adopt
containers that were not created by Bloud.

## Project Structure

```
services/host-agent/
├── cmd/host-agent/            # Entry point, bootstrap
├── internal/
│   ├── api/                   # HTTP server & routes (chi router)
│   ├── appconfig/             # Configurator registration
│   ├── catalog/               # App discovery from metadata.yaml
│   ├── config/                # Runtime configuration
│   ├── container/             # Container runtime abstraction
│   ├── db/                    # SQLite database + schema
│   ├── e2e/                   # Integration test helpers
│   ├── graph/                 # Dependency graph
│   ├── integration/           # Typed integration resolver
│   ├── logbuffer/             # Log buffering
│   ├── logfile/               # Log file management
│   ├── netutil/               # Network utilities
│   ├── orchestrator/          # Install/uninstall, intent queue, container management
│   ├── podman/                # Podman client
│   ├── secrets/               # Secrets manager
│   ├── sharing/               # Sharing & remote apps
│   ├── sso/                   # SSO/Authentik integration
│   ├── store/                 # SQLite persistence
│   ├── system/                # System state
│   ├── testdb/                # Test database helpers
│   └── traefikgen/            # Traefik route generation
├── pkg/
│   ├── authentik/             # Authentik REST API client
│   ├── configurator/          # Configurator interface + helpers
│   ├── managedfile/           # Managed file abstraction
│   ├── slug/                  # URL-safe slugs
│   └── xmlutil/               # XML utilities
└── web/                       # SvelteKit frontend
```

Key runtime concepts:
- **Catalog** reads `apps/*/metadata.yaml` at startup, caches in SQLite
- **Integration Resolver** binds provider apps to consumer requirements (database, SSO, proxy)
- **Intent queue** — all mutations flow through typed intents with debounce; the orchestrator is the single writer
- **Configurators** implement `PreStart`/`PostStart`/`Remove` per container node; container lifecycle is metadata-driven
- **App Store** (`internal/store/`) — SQLite persistence for installed apps, status, integration bindings
- **Container Runtime** — Podman containers created and managed directly by the orchestrator

## API Endpoints

### Health & System

- `GET /api/health` — Health check
- `GET /api/system/status` — System metrics (CPU, memory, disk)

### Apps

- `GET /api/apps` — List available apps from catalog
- `GET /api/apps/installed` — List installed apps with status
- `POST /api/apps/:name/install` — Install an app
- `POST /api/apps/:name/uninstall` — Uninstall an app (with optional `clearData`)
- `PUT /api/apps/:name/rename` — Rename an app

### Sharing

- `GET /sharing/shares` — List active shares
- `DELETE /sharing/shares/:id` — Revoke a share
- `GET /sharing/remote-apps` — List remote apps (from sharing guests)
- `POST /sharing/remote-apps` — Add a remote app
- `DELETE /sharing/remote-apps/:id` — Remove a remote app

### Integration Graph

- `GET /api/graph` — Developer graph visualization (app nodes, connection nodes, edges)

### Configuration

- `GET /api/settings` — Host settings
- `PUT /api/settings` — Update settings

## Testing

```bash
# Unit tests only
./bloud validate --tier fast

# Integration tests (requires Lima VM)
./bloud validate --tier integration

# E2E lifecycle (deploy + test + uninstall)
./bloud e2e lifecycle
```

```bash
# Smoke test API endpoints
curl http://localhost:3000/api/health
curl http://localhost:3000/api/apps/installed
```

## Deployment

The first-release target is a Debian package that installs the host-agent binary and a
systemd service. Local development can run the binary directly.

```bash
# Initialize a packaged installation
sudo bloud init

# Check service status
systemctl status bloud

# View logs
journalctl -u bloud -f

# Restart service
sudo systemctl restart bloud
```

## Troubleshooting

**Frontend not loading in production?**
- Make sure you ran `npm run build` in the `web/` directory before building the Go binary
- The Go binary embeds `web/build/` using `go:embed`

**Database errors?**
- Check that `$BLOUD_DATA_DIR/state/` exists and is writable
- Database is auto-created on first run

**Port already in use?**
- Change the port: `export BLOUD_PORT=3000`


