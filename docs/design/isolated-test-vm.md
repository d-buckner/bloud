# Isolated Test VM Design

## Overview

Create a completely isolated test environment using a separate NixOS VM that can run integration tests in parallel with the development environment on different ports.

## Problem Statement

Currently, tests and development share the same Lima VM:
- Can't run tests while developing (port conflicts)
- Tests may be affected by dev state
- No true isolation between test runs
- `FRESH_VM=true` is slow and disrupts dev work

## Solution

Run a separate test VM (`bloud-test`) alongside the dev VM (`bloud`) on different ports.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        macOS Host                                │
├─────────────────────────────────────┬───────────────────────────┤
│        Dev VM (bloud)               │     Test VM (bloud-test)  │
│                                     │                           │
│  Port 3000 → Go API                 │  Port 3001 → Go API       │
│  Port 5173 → Vite                   │  Port 5174 → Vite         │
│  Port 8080 → Traefik                │  Port 8081 → Traefik      │
│                                     │                           │
│  ./lima/dev                         │  ./lima/test              │
└─────────────────────────────────────┴───────────────────────────┘
```

## Port Mapping

| Service  | Dev Port | Test Port |
|----------|----------|-----------|
| Go API   | 3000     | 3001      |
| Vite     | 5173     | 5174      |
| Traefik  | 8080     | 8081      |

## Implementation

### 1. Lima Configuration for Test VM

**File**: `lima/test-nixos.yaml`

Copy from `lima/nixos.yaml` with modifications:
- Port forwards: 3001, 5174, 8081 (instead of 3000, 5173, 8080)
- Mount at `/home/bloud.linux/bloud-v3` (same path for code compatibility)
- Separate temp mount: `/tmp/lima-test`

```yaml
# Port forwarding for test environment
portForwards:
  - guestPort: 5174
    hostPort: 5174
  - guestPort: 3001
    hostPort: 3001
  - guestPort: 8081
    hostPort: 8081
```

### 2. NixOS Test Configuration

**File**: `nixos/vm-test.nix`

Copy from `vm-dev.nix` with modifications:

```nix
{ config, pkgs, lib, modulesPath, ... }:

{
  imports = [
    (modulesPath + "/profiles/qemu-guest.nix")
    ./modules/lima-init.nix
  ];

  networking = {
    hostName = "bloud-test";  # Different hostname
    useDHCP = true;
    firewall.allowedTCPPorts = [
      22    # SSH
      3001  # Host Agent API (test)
      5174  # Web Dev Server (test)
      8081  # Traefik (test)
    ];
  };

  # Same bloud user, same packages
  bloud = {
    enable = true;
    user = "bloud";
  };

  # Override Traefik port
  bloud.apps.traefik.port = 8081;

  # ... rest same as vm-dev.nix
}
```

### 3. Test VM Management Script

**File**: `lima/test`

Commands:
- `vm-create` - Create fresh test VM (deletes existing first)
- `vm-destroy` - Destroy test VM completely
- `start` - Start test services (Go + Vite on test ports)
- `stop` - Stop test services
- `status` - Check test VM status
- `shell` - SSH into test VM
- `rebuild` - Rebuild NixOS config

Key implementation details:
- VM name: `bloud-test`
- Config file: `test-nixos.yaml`
- Flake target: `#vm-test`
- tmux session: `bloud-test`

### 4. Test Start Script

**File**: `lima/start-test.sh`

Similar to `start-dev.sh` but with test-specific configuration:

```bash
# Test environment configuration
export BLOUD_PORT=3001
export BLOUD_DATA_DIR="/home/bloud/.local/share/bloud-test"

# Vite on test port
node vite.js dev --port 5174
```

### 5. Flake Configuration

**File**: `flake.nix`

Add test VM configuration:

```nix
nixosConfigurations = {
  vm-dev = nixpkgs.lib.nixosSystem {
    system = "aarch64-linux";
    modules = [ ./nixos/vm-dev.nix ./nixos/bloud.nix ];
  };

  vm-test = nixpkgs.lib.nixosSystem {
    system = "aarch64-linux";
    modules = [ ./nixos/vm-test.nix ./nixos/bloud.nix ];
  };
};
```

### 6. Updated Test Infrastructure

**File**: `integration/playwright.config.ts`

```typescript
export default defineConfig({
  use: {
    // Test environment ports
    baseURL: process.env.BASE_URL || 'http://localhost:8081',
  },
});
```

**File**: `integration/lib/global-setup.ts`

New behavior:
1. Create fresh test VM (`./lima/test vm-create`)
2. Start test services (`./lima/test start`)
3. Wait for services on test ports (3001, 5174, 8081)
4. No app state reset needed (fresh VM = clean state)

**File**: `integration/lib/global-teardown.ts`

New behavior:
1. Stop test services (`./lima/test stop`)
2. Destroy test VM (`./lima/test vm-destroy`)
3. `KEEP_TEST_VM=true` preserves VM for debugging

## Data Isolation

Test VM uses separate data directories:

| Purpose          | Dev Path                           | Test Path                              |
|------------------|------------------------------------|-----------------------------------------|
| Data dir         | `/home/bloud/.local/share/bloud/`  | `/home/bloud/.local/share/bloud-test/` |
| Traefik config   | `.../bloud/traefik/`               | `.../bloud-test/traefik/`              |
| Nix config       | `.../bloud/nix/`                   | `.../bloud-test/nix/`                  |

## File Changes Summary

| File | Action | Purpose |
|------|--------|---------|
| `scripts/run-integration-tests` | Create | Main test runner script |
| `lima/test-nixos.yaml` | Create | Lima config for test VM |
| `lima/test` | Create | Test VM management script |
| `lima/start-test.sh` | Create | Start services on test ports |
| `nixos/vm-test.nix` | Create | NixOS config with test ports |
| `flake.nix` | Modify | Add vm-test configuration |
| `integration/playwright.config.ts` | Modify | Use test ports |
| `integration/lib/global-setup.ts` | Modify | Support external VM lifecycle |
| `integration/lib/global-teardown.ts` | Modify | Support external VM lifecycle |
| `integration/lib/api-client.ts` | Modify | Default to test API port |
| `integration/package.json` | Modify | Use run-integration-tests script |

## Environment Variables

**New (test-specific):**
- `KEEP_TEST_VM=true` - Don't destroy VM after tests (for debugging)

**Removed (no longer needed):**
- `FRESH_VM` - Always fresh now
- `SKIP_VM_SETUP` - Test VM is always managed

## Usage

### Running tests (creates fresh VM each time)
```bash
cd integration
npm test
```

### Running tests while developing
```bash
# Terminal 1: Dev environment (ports 3000/5173/8080)
./lima/dev start

# Terminal 2: Tests (ports 3001/5174/8081)
cd integration && npm test
```

### Debugging failed tests
```bash
# Keep VM alive after tests
KEEP_TEST_VM=true npm test

# Then inspect
./lima/test shell
./lima/test logs
./lima/test status
```

### Manual test VM management
```bash
# Create test VM manually
./lima/test vm-create

# Start services
./lima/test start

# Run specific test
cd integration && npx playwright test tests/home.spec.ts

# Clean up
./lima/test vm-destroy
```

## Verification Checklist

After implementation, verify:

- [ ] `./lima/dev start` works on ports 3000/5173/8080
- [ ] `./lima/test vm-create` creates separate VM named `bloud-test`
- [ ] `./lima/test start` works on ports 3001/5174/8081
- [ ] Both VMs can run simultaneously without conflicts
- [ ] `npm test` creates fresh VM, runs tests, destroys VM
- [ ] `KEEP_TEST_VM=true npm test` preserves VM after tests
- [ ] All existing tests pass with new infrastructure
- [ ] Test VM uses separate data directory (`bloud-test/`)

## Future Considerations

- **CI Support**: Design allows easy extension to GitHub Actions using Linux VMs
- **Parallel Tests**: Could run multiple test VMs for parallel test execution
- **Snapshots**: Could use VM snapshots for faster fresh-state restoration
