# AGENTS.md — Bloud Dev Environment

## Getting Started Locally

The dev environment runs inside a **Lima VM** (`bloud-dev`) with a Go `host-agent` managing all containers.

### Quick Start

```bash
# 1. Ensure Lima VM is running
limactl list | grep bloud-dev    # should be "Running"
# If stopped: limactl start bloud-dev

# 2. One-time VM provisioning (only if never done)
limactl shell bloud-dev bash dev/setup.sh

# 3. Run the dev loop (rebuild + deploy + start)
./bloud dev
```

### Architecture

```
Host → Lima VM (bloud-dev, Debian 13, aarch64)
         └── host-agent (Go, port 3000)
               ├── Creates/starts containers directly via Podman API
               ├── Traefik dynamic route generation
               └── Web dashboard
         └── Containers (Podman):
               ├── traefik     :8080 (host network)  ← main entrypoint
               ├── postgres    :5432
               ├── redis       :6379
               ├── authentik     :9000
               ├── jellyfin    :8096
```

### Port Map

| Service | Port | Notes |
|---------|------|-------|
| Traefik (dashboard + proxy) | **8080** | Main entrypoint — `http://localhost:8080` |
| Host-agent API | 3000 | Internal management |
| PostgreSQL | 5432 | Shared DB |
| Authentik | 9000 | SSO provider |
| Jellyfin | 8096 | Media server |

**Traefik is on 8080, not 8000.**

### Lima VM Shell Quirk

The VM's bash can't parse single-quoted strings. Always wrap commands in `sh -c`:

```bash
# ❌ Fails:
limactl shell bloud-dev 'ls /some/path'

# ✅ Works:
limactl shell bloud-dev sh -c 'ls /some/path'
```

### Key VM Paths

| Path | Purpose |
|------|---------|
| `/var/tmp/bloud-dev-runtime/data/` | App data, secrets.json, traefik config |
| `/var/tmp/bloud-dev-runtime/data/traefik/dynamic/` | Generated Traefik routes |
| `/var/tmp/bloud-dev-runtime/host-agent/` | Deployed binary + frontend |

### Environment Variables (host-agent)

```bash
BLOUD_DATA_DIR=/var/tmp/bloud-dev-runtime/data
BLOUD_APPS_DIR=/Users/daniel/Projects/bloud/apps
BLOUD_TRAEFIK_DYNAMIC_DIR=/var/tmp/bloud-dev-runtime/data/traefik/dynamic
```

### Troubleshooting

```bash
# Reset everything (keeps VM, wipes data)
./bloud reset && ./bloud dev

# Destroy VM entirely and start fresh
./bloud destroy
limactl create --name=bloud-dev dev/lima.yaml
limactl start bloud-dev
limactl shell bloud-dev bash dev/setup.sh
./bloud dev

# Check VM shell access
limactl shell bloud-dev sh -c 'id'

# Check running containers
limactl shell bloud-dev sh -c 'podman ps -a'
```

### What `./bloud dev` Does

1. Stops existing app containers, removes them
2. Cross-compiles host-agent for `linux/arm64`
3. Builds Svelte frontend
4. Deploys binary + frontend to VM at `/var/tmp/bloud-dev-runtime/`
5. Starts host-agent in foreground (Ctrl-C to stop)

### Manual Host-Agent Start (if CLI fails)

```bash
# Build
cd services/host-agent
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/host-agent ./cmd/host-agent

# Deploy
limactl copy /tmp/host-agent bloud-dev:/var/tmp/bloud-dev-runtime/host-agent/host-agent

# Run
limactl shell bloud-dev bash -c '
  cd /var/tmp/bloud-dev-runtime/host-agent && \
  BLOUD_DATA_DIR=/var/tmp/bloud-dev-runtime/data \
  BLOUD_APPS_DIR=/Users/daniel/Projects/bloud/apps \
  BLOUD_TRAEFIK_DYNAMIC_DIR=/var/tmp/bloud-dev-runtime/data/traefik/dynamic \
  exec ./host-agent'
```

### After Starting

Once the host-agent is running, apps can be installed via the web dashboard at `http://localhost:8080` or via the API:

```bash
curl -X POST http://localhost:3000/api/apps/<name>/install
curl -X POST http://localhost:3000/api/apps/<name>/configure/poststart
```

### Project Structure (Relevant Parts)

```
bloud/
├── cli/
│   ├── main.go           # CLI entrypoint + command dispatch
│   └── dev.go            # ./bloud dev implementation
├── services/host-agent/
│   ├── cmd/host-agent/   # Go server + configure subcommands
│   ├── internal/
│   │   ├── config/       # Config loading (env + secrets)
│   │   ├── traefikgen/   # Traefik route generation
│   │   ├── orchestrator/ # Container management via Podman API
│   │   └── sso/          # OIDC/LDAP integration
│   └── web/              # Svelte frontend
├── apps/                 # App definitions (metadata.yaml, integration.md)
│   ├── traefik/
│   ├── authentik/
│   ├── jellyfin/
│   └── ...
├── dev/
│   ├── compose.yml       # Manual podman-compose stack (reference only)
│   ├── setup.sh          # One-time VM provisioning
│   ├── lima.yaml         # Lima VM definition
│   └── authentik-proxy.conf  # Nginx reverse proxy for authentik
└── LOCAL_DEV.md          # Detailed dev environment documentation
```
