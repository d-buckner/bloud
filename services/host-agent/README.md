# Bloud Host Agent

> **Current implementation reference:** This README describes the host-agent development
> workflow. [docs/specs/spec.md](../../docs/specs/spec.md) is the authoritative first-release plan.

Go service that manages app installation, system monitoring, and provides a web UI for the Bloud home server platform.

## Architecture

- **Backend**: Go HTTP server with SQLite database
- **Frontend**: SvelteKit built as a static site by `@sveltejs/adapter-static` into `web/build/`; host-agent serves that directory at `/`. There is no server-side rendering: the browser runs the app and talks to the API.
- **Deployment**: One Go binary, run in the foreground during development and under systemd in a deployment

## Prerequisites

- **Go 1.25** (the version in `go.mod`) - [Install Go](https://go.dev/doc/install)
- **Node.js 18+** - [Install Node](https://nodejs.org/)
- **npm** (the frontend is the npm workspace `@bloud/host-agent-web`)
- Rootless **Podman** with an API socket host-agent can reach (`BLOUD_PODMAN_SOCKET`, defaulting to the Podman socket for the current user)

## Development

### Install dependencies

```bash
# From the repo root (npm workspaces)
npm install

# Go modules are downloaded on the first build; pre-fetch them explicitly with:
cd services/host-agent && go mod download
```

### Full loop (recommended)

From the repo root:

```bash
./bloud dev
```

One command provisions the development VM if needed, builds host-agent (`CGO_ENABLED=0 GOOS=linux`)
and the frontend, deploys both into the VM, and runs host-agent in the foreground (Ctrl-C stops it).
Ports are forwarded to host localhost. **There is no back-end hot reload: re-run `./bloud dev`
after any Go change.**

- **Dashboard (user-facing, through Traefik)**: http://localhost:8080
- **Host-agent API**: http://localhost:3000/api/health
- **Host-agent direct**: http://localhost:3000

### Frontend only

```bash
cd services/host-agent/web
npm run dev    # Vite dev server on port 5173, with HMR
npm run build  # static build into web/build/
```

`vite.config.ts` declares no `server.proxy`, and the frontend code issues relative `/api/...`
requests, so the 5173 dev server renders the UI but does not serve the API. Use `./bloud dev`
for a loop that includes the backend, or let host-agent serve the built output itself. Front-end
HMR in `npm run dev` is live, so component work does not need a redeploy.

### Backend directly

```bash
cd services/host-agent

# Run host-agent on this machine (needs a reachable Podman socket)
npm run dev

# Build a production-style binary
npm run build
```

host-agent serves the frontend from `web/build` **relative to its working directory**, so run it
from `services/host-agent` after building the frontend. When that directory is missing it logs a
warning and serves the small embedded developer dashboard instead, without the real UI.

### Environment Variables

```bash
export BLOUD_PORT=3000                          # HTTP port (default: 3000)
export BLOUD_DATA_DIR=$HOME/.local/share/bloud  # Data directory (default shown)
export BLOUD_APPS_DIR="$(pwd)/../../apps"       # Catalog directory (default)
```

Configuration is env var first, then `secrets.json`, then an error: there is no hardcoded fallback
(see AGENTS.md invariant 8 for the full key list).

### Data Directory

Everything host-agent persists lives under `$BLOUD_DATA_DIR`:

- `bloud.db`: the SQLite database, created on first run and opened in WAL mode with foreign keys on
- `secrets.json`: generated secrets (Postgres password, SSO host secret, LDAP bind password, admin API token). It is created on first run and migrated on load; a corrupt file is a fatal error rather than a silent regeneration.
- `host-agent-api-token`: the admin API token as a standalone file, written next to `secrets.json` so the CLI and tests never parse the secrets file

The schema comes from the embedded `internal/schema/schema.sql`, applied through the versioned
migration ledger in `internal/schema/migrations.go` on every start. Both the production database and
test databases run that same ledger, so they cannot drift apart.

## Building for Production

### 1. Build the frontend

```bash
cd services/host-agent/web
npm run build
# Creates: web/build/
```

### 2. Build the Go binary

```bash
cd services/host-agent
go build -o bin/host-agent ./cmd/host-agent
# Creates: bin/host-agent
```

The binary does not embed the frontend. It serves `<working directory>/web/build` at `/`, so ship
`web/build` next to the binary and start the process from that directory.

### 3. Run the binary

```bash
cd services/host-agent
./bin/host-agent
```

Bloud runs directly against an accessible Podman API socket:

```bash
export BLOUD_PODMAN_SOCKET="${XDG_RUNTIME_DIR}/podman/podman.sock"
export BLOUD_DATA_DIR="${HOME}/.local/share/bloud"
export BLOUD_APPS_DIR="$(pwd)/../../apps"
./bin/host-agent
```

Bloud owns managed application containers and networks, creating and starting them directly through
the Podman API. Containers carry the label `io.bloud.managed=true`, and host-agent refuses to remove
a container that lacks it.

## Project Structure

```
services/host-agent/
├── cmd/host-agent/            # Entry point, bootstrap, `configure` and `init-secrets` subcommands
├── internal/
│   ├── api/                   # HTTP server, routes, auth, one module per domain
│   ├── appconfig/             # Configurator registration
│   ├── catalog/               # App discovery from metadata.yaml, planning, dependency graph
│   ├── config/                # Runtime configuration and secrets resolution
│   ├── container/             # Container runtime abstraction and managed-label checks
│   ├── db/                    # SQLite connection, pragmas, migration entry point
│   ├── e2e/                   # In-guest integration test helpers
│   ├── engine/
│   │   ├── graph/             # Dependency graph repository
│   │   └── orchestrator/      # Intent queue, reconciliation, install/uninstall
│   ├── eventbus/              # In-process event bus behind the SSE streams
│   ├── hostset/               # Live host-set state for multi-host SSO
│   ├── netutil/               # Network utilities
│   ├── podman/                # Podman API client
│   ├── schema/                # Embedded schema.sql + versioned migration ledger
│   ├── secrets/               # Secrets manager and env-file generation
│   ├── sharing/               # Sharing & remote apps
│   ├── sso/                   # SSO/Authentik integration
│   ├── store/                 # SQLite persistence
│   ├── system/                # Cached system metrics (CPU, memory, disk)
│   ├── testdb/                # Test database helpers
│   └── traefikgen/            # Traefik route generation
├── pkg/
│   ├── appasset/              # Static/downloaded asset install (fetch, sha256 verify, atomic commit)
│   ├── appclient/             # Resilient HTTP client for app APIs (retries, token refresh)
│   ├── authentik/             # Authentik REST API client
│   ├── configurator/          # Configurator interface + helpers
│   ├── managedfile/           # Marker-delimited managed regions in user-owned files
│   ├── slug/                  # URL-safe slug generation for subdomain routing
│   └── xmlutil/               # Utilities for reading and modifying XML config files
└── web/                       # SvelteKit frontend
```

Key runtime concepts:
- **Catalog** reads `apps/*/metadata.yaml` at startup and caches the result in SQLite
- **Dependency graph**: each `containers:` entry is one node, `dependsOn` builds the DAG, and the orchestrator converges nodes in topological order
- **Intent queue**: all mutations flow through typed intents with a 750 ms debounce window; the orchestrator is the single writer
- **Configurators** implement `PreStart`/`PostStart`/`Remove` per container node and must be idempotent, since they run on every reconciliation cycle; container lifecycle is metadata-driven
- **App Store** (`internal/store/`): SQLite persistence for installed apps, lifecycle status, operations, sessions, hosts, shares, guests, remote apps, and dashboard layout positions
- **Container Runtime**: Podman containers created and managed directly by the orchestrator

## API Endpoints

Authentication is a session cookie or the admin API token presented from a trusted position
(loopback or a `BLOUD_TRUSTED_LOCAL_NETS` address). Status codes below are the documented contract;
lifecycle mutations are accepted as intents, so they answer `202 Accepted` with an intent id and the
result arrives on the event stream or by polling.

### Health & System (public)

- `GET /health`: Health check, no `/api` prefix
- `GET /api/health`: Health check
- `GET /api/system/status`: System metrics (CPU, memory, disk)
- `GET /api/system/storage`: Storage breakdown
- `GET /api/system/developer`: Developer graph visualization (app nodes, connection nodes, edges)
- `GET /api/system/status/stream`: Server-sent stream of system status (authenticated)

### Authentication & Setup (public)

- `GET /auth/login`, `GET /auth/callback`, `POST /auth/logout`: Authentik login flow, no `/api` prefix
- `GET /api/auth/me`: Current user
- `GET /api/setup/status`, `POST /api/setup/create-user`: First-run bootstrap, reachable before any credential exists

### Apps (authenticated)

- `GET /api/apps`: List available apps from the catalog
- `GET /api/apps/installed`: List installed apps with status
- `GET /api/apps/:name/metadata`: Full metadata for one catalog app
- `GET /api/apps/:name/icon`: App icon
- `GET /api/apps/events`: Server-sent app status stream
- `GET /api/apps/:name/logs`: Server-sent container log stream
- `POST /api/apps/:name/install`: Install an app (`202`)
- `POST /api/apps/:name/uninstall`: Uninstall an app (`202`, optional `clearData` in the body)
- `PATCH /api/apps/:name/rename`: Rename an app (`202`)

### Dashboard (authenticated)

- `GET /api/user/home`: Home layout
- `PUT /api/user/layout`: Persist the home layout (called after every drag/resize)

### Admin

- `POST /api/apps/refresh-catalog`: Reload the catalog from disk
- `GET /api/system/rebuild/stream`: Server-sent frontend rebuild stream
- `GET /api/settings/hosts`, `PUT /api/settings/hosts`: Host settings (the host set is a first-class setting; see AGENTS.md invariant 9)
- `GET /api/settings/tailnet`, `POST /api/settings/tailnet`, `DELETE /api/settings/tailnet`: Tailnet connection for sharing
- `GET /api/admin/users`, `POST /api/admin/users`, `DELETE /api/admin/users/:username`, `PUT /api/admin/users/:username/role`: User management

### Sharing (admin)

- `GET /api/sharing/community`, `POST /api/sharing/invites`: Community graph and invites
- `GET /api/sharing/shares`, `DELETE /api/sharing/shares/:id`: List and revoke shares
- `GET /api/sharing/guests`, `POST /api/sharing/guests`: List and create guests
- `GET /api/sharing/remote-apps`, `POST /api/sharing/remote-apps`, `DELETE /api/sharing/remote-apps/:id`: Remote apps from sharing guests

## Testing

```bash
# Unit tests only
./bloud validate --tier fast

# Integration tests (requires the VM)
./bloud validate --tier integration

# E2E lifecycle (deploy + test + uninstall)
./bloud e2e lifecycle
```

Run a suite directly when iterating on one package:

```bash
cd services/host-agent && go test ./...
npm run test --workspace=@bloud/host-agent-web    # vitest
cd e2e && npx playwright test                     # browser e2e
```

```bash
# Smoke test API endpoints
curl http://localhost:3000/api/health
curl http://localhost:3000/api/apps/installed
```

## Deployment

There is no packaged release yet: a `.deb` package and a `bloud init` preflight command are part of
the first-release plan in [docs/specs/spec.md](../../docs/specs/spec.md), not something you can run
today. Right now the supported way to run Bloud is the development VM, driven by the CLI:

```bash
./bloud dev      # build, deploy, run host-agent in the foreground (Ctrl-C to stop)
./bloud status   # VM + host-agent health (GET :3000/api/health)
./bloud logs     # stream host-agent logs
./bloud stop     # stop host-agent
./bloud reset    # wipe runtime data, keep the VM
./bloud destroy  # delete the VM
```

## Troubleshooting

**The dashboard shows the developer placeholder instead of the app?**
- host-agent logged "frontend build directory not found, serving fallback HTML", which means `web/build` was absent when it started
- Build the frontend (`npm run build` in `web/`) or run `./bloud dev`, which builds it for you

**Database errors?**
- Check that `$BLOUD_DATA_DIR` exists and is writable. `bloud.db` is created on first run, and a corrupt `secrets.json` makes startup fail rather than regenerating it

**Admin API calls return 401 or 403?**
- Send the token from `$BLOUD_DATA_DIR/host-agent-api-token` as a bearer credential from loopback or a `BLOUD_TRUSTED_LOCAL_NETS` address; the token is only honoured from a trusted position

**Port already in use?**
- Change the port: `export BLOUD_PORT=3001`
