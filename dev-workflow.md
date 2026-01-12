# Development Workflow

**Fast feedback loops for NixOS, Go services, and Frontend development**

> **Note:** This document describes the *planned* development workflow. The scripts referenced
> (e.g., `scripts/nixos-deploy-test.sh`) have not yet been created. For current development,
> use the Lima VM workflow documented in `CLAUDE.md` and `lima/README.md` with `./lima/dev`.

---

## Overview

Three independent development tracks with different workflows:

| Track | Parallelism | Environment | Validation |
|-------|-------------|-------------|------------|
| **NixOS** | Serial (one agent) | Remote dev machine | Deploy → Test (2 min) |
| **Go Services** | Parallel (multiple agents) | Local | Unit tests (instant) |
| **Frontend** | Parallel (multiple agents) | Local | Dev server (instant) |

**Key insight:** Natural separation prevents conflicts without complex tooling.

---

## Track 1: NixOS Development

**Use case:** Adding apps, modifying infrastructure, changing system config

**Constraint:** One agent at a time (NixOS rebuilds are system-wide)

### Fast Deploy/Test Cycle

**Goal:** < 2 minutes from code change → validated on real NixOS

#### Script: `scripts/nixos-deploy-test.sh`

```bash
#!/usr/bin/env bash
set -e

REMOTE_HOST="${NIXOS_DEV_HOST:-agent-dev@nixos-dev.local}"
REMOTE_DIR="/home/agent-dev/bloud-test"

echo "=== Deploying to ${REMOTE_HOST} ==="

# Step 1: Rsync code to remote (5-10 seconds)
echo "[1/4] Syncing code..."
rsync -avz --delete \
  --exclude='.git' \
  --exclude='result' \
  --exclude='*.qcow2' \
  --exclude='node_modules' \
  --exclude='worktree-*' \
  ./ "${REMOTE_HOST}:${REMOTE_DIR}/"

# Step 2: Remote validation and build
echo "[2/4] Running tests on remote..."
ssh "${REMOTE_HOST}" <<'REMOTE_SCRIPT'
set -e
cd ~/bloud-test

# Syntax check (5-10 seconds)
echo "  → Checking Nix syntax..."
nix flake check --show-trace 2>&1 | head -50 || {
  echo "❌ Syntax check failed"
  exit 1
}

# Build (30-60 seconds, cached builds much faster)
echo "  → Building system..."
nix build .#nixosConfigurations.bloud-test.config.system.build.toplevel \
  --show-trace --print-build-logs 2>&1 | tail -20 || {
  echo "❌ Build failed"
  exit 1
}

# Activate (rebuild switch - applies config)
echo "  → Activating configuration..."
sudo nixos-rebuild test --flake .#bloud-test 2>&1 | tail -10 || {
  echo "❌ Activation failed"
  exit 1
}

echo "✅ Build and activation successful"
REMOTE_SCRIPT

# Step 3: Validate services
echo "[3/4] Validating services..."
ssh "${REMOTE_HOST}" <<'REMOTE_SCRIPT'
set -e

# Wait for services to settle
sleep 5

# Check critical services
echo "  → Checking service status..."
systemctl --user status podman-traefik.service --no-pager --lines=5 || true
systemctl --user status podman-authentik-server.service --no-pager --lines=5 || true

# Health checks
echo "  → Running health checks..."
curl -sf http://localhost:8080/health && echo "  ✓ Traefik healthy" || echo "  ✗ Traefik failed"
curl -sf http://localhost:9000/api/v3/ && echo "  ✓ Authentik healthy" || echo "  ✗ Authentik failed"

echo "✅ Service validation complete"
REMOTE_SCRIPT

echo "[4/4] Done!"
echo ""
echo "Access services at:"
echo "  http://nixos-dev.local:8080  (Traefik)"
echo "  http://nixos-dev.local:9000  (Authentik)"
```

### Usage

```bash
# Make changes to NixOS config
vim nixos/apps/immich.nix

# Deploy and test
./scripts/nixos-deploy-test.sh

# Output:
# === Deploying to agent-dev@nixos-dev.local ===
# [1/4] Syncing code... (5s)
# [2/4] Running tests on remote...
#   → Checking Nix syntax... ✅
#   → Building system... ✅ (45s)
#   → Activating configuration... ✅ (10s)
# [3/4] Validating services...
#   ✓ Traefik healthy
#   ✓ Authentik healthy
# [4/4] Done! (Total: 1m 15s)
```

### Remote Machine Setup

**One-time setup on NixOS dev machine:**

```bash
# Create agent-dev user
sudo useradd -m -s /bin/bash agent-dev
echo "agent-dev ALL=(ALL) NOPASSWD: /run/current-system/sw/bin/nixos-rebuild" | \
  sudo tee /etc/sudoers.d/agent-dev

# Setup SSH key
sudo mkdir -p /home/agent-dev/.ssh
sudo cp ~/.ssh/authorized_keys /home/agent-dev/.ssh/
sudo chown -R agent-dev:agent-dev /home/agent-dev/.ssh
sudo chmod 700 /home/agent-dev/.ssh
sudo chmod 600 /home/agent-dev/.ssh/authorized_keys

# Create workspace
sudo -u agent-dev mkdir -p /home/agent-dev/bloud-test

# Enable lingering (keep user systemd running)
sudo loginctl enable-linger agent-dev
```

**NixOS configuration for fast builds:**

```nix
# /etc/nixos/configuration.nix (on dev machine)
{
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    max-jobs = 4;
    cores = 4;

    # Use binary cache for faster builds
    substituters = [
      "https://cache.nixos.org"
      "https://nix-community.cachix.org"
    ];
  };

  # More build parallelism
  nix.buildCores = 0;  # Use all cores
}
```

---

## Track 2: Go Services Development

**Use case:** Building host agent and shared libraries

**Parallelism:** Multiple agents on different services/packages

### Local Development Setup

**Project structure:**
```
bloud-v3/
├── services/
│   ├── host-agent/           # Host agent service
│   │   ├── main.go
│   │   ├── api/              # REST API handlers
│   │   ├── apps/             # App management
│   │   ├── nixgen/           # Nix config generation
│   │   └── db/               # SQLite interactions
│   └── shared/               # Shared libraries
│       ├── models/           # Common data models
│       └── utils/
└── go.mod
```

### Development Workflow

**Agent working on host agent:**
```bash
cd services/host-agent

# Run locally
go run main.go

# Run with hot reload (using air)
air

# Run tests
go test ./...

# Run specific test
go test -v -run TestAppInstall ./apps

# Test with coverage
go test -cover ./...
```

**Agent working on shared library:**
```bash
cd services/shared/auth

# Run tests
go test -v

# Test from dependent service
cd ../../host-agent
go test ./... # Uses shared/auth
```

### Fast Test/Validation

**Script: `scripts/go-test.sh`**

```bash
#!/usr/bin/env bash
set -e

SERVICE=${1:-all}

if [ "$SERVICE" = "all" ]; then
  echo "Running all Go tests..."
  go test ./services/... -v
else
  echo "Running tests for $SERVICE..."
  go test ./services/$SERVICE/... -v
fi

echo ""
echo "Running go vet..."
go vet ./services/...

echo ""
echo "Checking formatting..."
gofmt -l ./services/ | grep . && {
  echo "❌ Files need formatting (run: gofmt -w ./services/)"
  exit 1
} || echo "✅ All files formatted"

echo ""
echo "✅ All checks passed"
```

**Usage:**
```bash
# Test everything
./scripts/go-test.sh

# Test specific service
./scripts/go-test.sh host-agent

# Output:
# Running tests for host-agent...
# ok      bloud/services/host-agent/api    0.123s
# ok      bloud/services/host-agent/apps   0.456s
# ✅ All checks passed
```

### No Conflicts

**Multiple agents work independently:**
- Agent 1: `services/host-agent/api/apps.go` (add GET /api/apps endpoint)
- Agent 2: `services/host-agent/apps/install.go` (app installation logic)
- Agent 3: `services/shared/utils/helpers.go` (shared utilities)

**All can test locally, no shared state, no conflicts!**

---

## Track 3: Frontend Development

**Use case:** Building web UI (SvelteKit)

**Parallelism:** Multiple agents on different pages/components

### Local Development Setup

**Project structure:**
```
bloud-v3/
└── frontend/
    ├── src/
    │   ├── routes/
    │   │   ├── +page.svelte           # Dashboard
    │   │   ├── apps/
    │   │   │   └── +page.svelte        # App catalog
    │   │   ├── settings/
    │   │   │   └── +page.svelte        # Settings
    │   │   └── hosts/
    │   │       └── +page.svelte        # Multi-host view
    │   ├── lib/
    │   │   ├── components/
    │   │   │   ├── AppCard.svelte
    │   │   │   ├── ServiceStatus.svelte
    │   │   │   └── SystemStats.svelte
    │   │   └── api/
    │   │       └── client.ts           # API client
    │   └── app.html
    ├── package.json
    └── svelte.config.js
```

### Development Workflow

**Agent working on dashboard:**
```bash
cd frontend

# Install dependencies (first time)
npm install

# Start dev server
npm run dev

# Navigate to http://localhost:5173
# Edit src/routes/+page.svelte
# See changes instantly (HMR)
```

**Agent working on app catalog:**
```bash
cd frontend

# Start dev server
npm run dev

# Edit src/routes/apps/+page.svelte
# Edit src/lib/components/AppCard.svelte
# Changes reflect instantly
```

**Agent working on components:**
```bash
cd frontend

# Run component tests
npm run test

# Run tests in watch mode
npm run test:watch

# Test specific component
npm run test -- AppCard.test.ts
```

### Fast Test/Validation

**Script: `scripts/frontend-test.sh`**

```bash
#!/usr/bin/env bash
set -e

cd frontend

echo "=== Frontend Validation ==="

echo "[1/4] Checking TypeScript..."
npm run check

echo "[2/4] Running linter..."
npm run lint

echo "[3/4] Running tests..."
npm run test

echo "[4/4] Building production bundle..."
npm run build

echo ""
echo "✅ All frontend checks passed"
echo "Build size:"
npx vite-bundle-visualizer --template sunburst --open=false
```

**Usage:**
```bash
./scripts/frontend-test.sh

# Output:
# === Frontend Validation ===
# [1/4] Checking TypeScript... ✅
# [2/4] Running linter... ✅
# [3/4] Running tests... ✅
# [4/4] Building production bundle... ✅
# Build size: 234 KB
```

### No Conflicts

**Multiple agents work independently:**
- Agent 1: `src/routes/+page.svelte` (dashboard layout)
- Agent 2: `src/routes/apps/+page.svelte` (app catalog page)
- Agent 3: `src/lib/components/AppCard.svelte` (app card component)

**All run local dev server on different ports, no conflicts!**

---

## Integration Testing

**When all pieces come together:**

### End-to-End Test Environment

**Setup:**
```
1. NixOS dev machine: Running host agent + apps
2. Local Frontend: SvelteKit dev server (npm run dev)
```

**Full stack test:**
```bash
# Terminal 1: Start frontend dev server
cd frontend
npm run dev
# Listening on :5173

# Terminal 2: Deploy NixOS config to dev machine
./scripts/nixos-deploy-test.sh
# Host agent running on nixos-dev.local:8080

# Terminal 3: Run integration tests
./scripts/integration-test.sh
```

**Script: `scripts/integration-test.sh`**

```bash
#!/usr/bin/env bash
set -e

NIXOS_HOST="nixos-dev.local:8080"
FRONTEND_HOST="localhost:5173"

echo "=== Integration Tests ==="

echo "[1/3] Testing host agent API..."
curl -sf "http://${NIXOS_HOST}/api/apps" | jq . || {
  echo "❌ Host agent not responding"
  exit 1
}

echo "[2/3] Testing frontend..."
curl -sf "http://${FRONTEND_HOST}" > /dev/null || {
  echo "❌ Frontend not responding"
  exit 1
}

echo "[3/3] Testing app installation flow..."
npx playwright test e2e/install-app.spec.ts

echo ""
echo "✅ All integration tests passed"
```

---

## Quick Reference

### For NixOS Agent

```bash
# Make changes
vim nixos/apps/immich.nix

# Deploy and test
./scripts/nixos-deploy-test.sh

# Check logs on remote
ssh agent-dev@nixos-dev.local "journalctl --user -u podman-immich -f"
```

### For Go Service Agent

```bash
# Make changes
vim services/host-agent/api/apps.go

# Run tests
go test ./services/host-agent/...

# Run locally
cd services/host-agent && go run main.go
```

### For Frontend Agent

```bash
# Make changes
vim frontend/src/routes/apps/+page.svelte

# Auto-reloads in browser
# Run tests
cd frontend && npm run test

# Build
npm run build
```

### For Integration Testing

```bash
# Start all services
./scripts/start-dev-stack.sh

# Run integration tests
./scripts/integration-test.sh
```

---

## Performance Targets

| Operation | Target | Actual |
|-----------|--------|--------|
| NixOS syntax check | < 10s | ~5s |
| NixOS build (cached) | < 30s | ~20s |
| NixOS build (uncached) | < 2m | ~1m 30s |
| Go unit tests | < 5s | ~2s |
| Frontend HMR update | < 500ms | ~200ms |
| Frontend build | < 30s | ~15s |
| Full integration test | < 5m | ~3m |

---

## Troubleshooting

### NixOS deploy fails with "permission denied"

**Solution:**
```bash
# On remote machine, ensure agent-dev has sudo access
sudo visudo
# Add: agent-dev ALL=(ALL) NOPASSWD: /run/current-system/sw/bin/nixos-rebuild
```

### Go tests fail with "database not found"

**Solution:**
```bash
# Create test database
createdb bloud_test

# Run tests with test DB
export DATABASE_URL="postgresql://localhost/bloud_test"
go test ./...
```

### Frontend dev server won't start

**Solution:**
```bash
# Clear node_modules and reinstall
cd frontend
rm -rf node_modules package-lock.json
npm install
npm run dev
```

### Integration tests timeout

**Solution:**
```bash
# Check all services are running
curl http://nixos-dev.local:8080/health
curl http://localhost:8000/health
curl http://localhost:5173

# Increase timeout in playwright.config.ts
```

---

## Next Steps

1. **Create scripts:**
   ```bash
   mkdir -p scripts/
   touch scripts/nixos-deploy-test.sh
   touch scripts/go-test.sh
   touch scripts/frontend-test.sh
   touch scripts/integration-test.sh
   chmod +x scripts/*.sh
   ```

2. **Setup remote NixOS machine:**
   ```bash
   ssh nixos-dev.local
   # Run setup commands from "Remote Machine Setup" section
   ```

3. **Initialize Go project:**
   ```bash
   mkdir -p services/{host-agent,shared}
   go mod init github.com/yourusername/bloud
   ```

4. **Initialize Frontend:**
   ```bash
   npm create svelte@latest frontend
   cd frontend && npm install
   ```

5. **Test the workflow:**
   - Make a small NixOS change → deploy → validate
   - Write a simple Go test → run locally
   - Create a basic Svelte page → see in browser

---

**Document Owner:** Daniel
**Created:** January 2026
**Status:** Ready to implement
