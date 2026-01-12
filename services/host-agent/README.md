# Bloud Host Agent

Go service that manages app installation, system monitoring, and provides a web UI for the Bloud home server platform.

## Architecture

- **Backend**: Go HTTP server with SQLite database
- **Frontend**: SvelteKit with SSR/SSG (embedded in Go binary)
- **Deployment**: Native systemd service on NixOS (production) or standalone (development)

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

**Option A: Run both frontend and backend together (recommended)**

From the project root:

```bash
# This runs frontend dev server on :5173 and you run Go separately
npm run frontend:dev
```

Then in another terminal:

```bash
# Run Go backend on :8080
npm run backend:dev
```

The frontend dev server (port 5173) will proxy API requests to the Go backend (port 8080).

**Option B: Run them separately**

Terminal 1 - Go Backend:
```bash
cd services/host-agent
go run ./cmd/host-agent
```

Terminal 2 - Frontend:
```bash
cd services/host-agent/web
npm run dev
```

### 3. Access the Application

- **Frontend (dev)**: http://localhost:5173
- **Backend API**: http://localhost:8080/api/health
- **Backend direct**: http://localhost:8080

## Development Workflow

### Frontend Development

The frontend is a SvelteKit app with:
- **SSG** - Static site generation for fast loading
- **API Proxy** - Dev server proxies `/api/*` to Go backend
- **Hot Reload** - Instant updates on file changes

```bash
cd services/host-agent/web

# Start dev server
npm run dev

# Build for production (creates web/build/)
npm run build
```

### Backend Development

The Go backend serves:
- REST API endpoints
- Embedded frontend (when built)
- WebSocket for real-time updates

```bash
cd services/host-agent

# Run with auto-reload (requires air)
go install github.com/cosmtrek/air@latest
air

# Or run directly
go run ./cmd/host-agent

# Build binary
go build -o bin/host-agent ./cmd/host-agent
```

### Environment Variables

```bash
# Optional configuration
export BLOUD_PORT=8080                          # HTTP port (default: 8080)
export BLOUD_DATA_DIR=$HOME/.local/share/bloud  # Data directory
```

### Database

SQLite database is automatically created at:
```
$HOME/.local/share/bloud/state/bloud.db
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

## Project Structure

```
services/host-agent/
├── cmd/
│   └── host-agent/
│       └── main.go              # Entry point
├── internal/
│   ├── api/                     # HTTP server & routes
│   │   ├── server.go
│   │   └── routes.go
│   ├── db/                      # SQLite database
│   │   ├── db.go
│   │   └── schema.sql
│   └── config/                  # Configuration
│       └── config.go
├── web/                         # SvelteKit frontend
│   ├── src/
│   │   └── routes/
│   │       └── +page.svelte     # Dashboard
│   ├── package.json
│   └── svelte.config.js
├── go.mod
└── README.md
```

## API Endpoints

### Health & Status

- `GET /api/health` - Health check
- `GET /api/system/status` - System metrics (CPU, memory, disk)

### Apps

- `GET /api/apps` - List available apps from catalog
- `GET /api/apps/installed` - List installed apps

### Future Endpoints

- `POST /api/apps/:name/install` - Install an app
- `POST /api/apps/:name/uninstall` - Uninstall an app
- `GET /api/hosts` - List discovered hosts (multi-host)

## Testing

```bash
# Test API endpoints
curl http://localhost:8080/api/health
curl http://localhost:8080/api/apps/installed

# Test frontend
open http://localhost:5173
```

## Deployment (NixOS)

The host agent is deployed as a Podman container via NixOS. See `apps/host-agent/module.nix` for the service definition.

```bash
# On NixOS system
sudo nixos-rebuild switch

# Check service status
systemctl --user status podman-host-agent

# View logs
journalctl --user -u podman-host-agent -f

# Restart service
systemctl --user restart podman-host-agent
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

## Next Steps

- [ ] Implement app catalog loader
- [ ] Add NixOS config generator
- [ ] Implement app installation/uninstall
- [ ] Add WebSocket for real-time updates
- [ ] Implement mDNS discovery for multi-host
- [ ] Add system monitoring (CPU, memory, disk)
