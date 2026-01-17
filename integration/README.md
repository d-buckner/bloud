# Bloud Integration Tests

End-to-end integration tests using Playwright.

## Setup

```bash
cd integration
npm install
npm run install-browsers
```

## Running Tests

### Standard Run (starts VM if needed, resets app state)

```bash
npm test
```

This will:
1. Start the Lima VM if not running
2. Start dev services (air + vite)
3. Uninstall all user apps for a clean slate
4. Run all tests
5. Stop dev services (VM stays running)

### Fresh VM (completely clean state)

```bash
npm run test:fresh
```

This deletes and recreates the VM from scratch. Takes longer but guarantees pristine state.

### Quick Run (skip VM management)

```bash
npm run test:quick
```

Use this when you already have the dev environment running via `./bloud start`.
Runs tests against the current state without resetting.

### Keep Services Running After Tests

```bash
npm run test:keep
```

Useful for debugging - services stay running so you can inspect the UI.

### Interactive Debugging

```bash
# Run with browser visible
npm run test:headed

# Pause on each step
npm run test:debug

# Playwright UI mode
npm run test:ui
```

### Run Specific Tests

```bash
# Run a specific test file
npx playwright test tests/catalog.spec.ts

# Run tests matching a pattern
npx playwright test -g "can install app"
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `KEEP_TEST_VM=true` | Keep test VM running after tests (for debugging) |
| `SKIP_VM_LIFECYCLE=true` | Skip VM management (used by `test:quick`) |
| `BASE_URL` | Override web UI URL (default: http://localhost:8081) |
| `API_URL` | Override API URL (default: http://localhost:3001) |

## Test Organization

```
tests/
  catalog.spec.ts          # App catalog and installation
  home.spec.ts             # Home page and app launcher
  embedded-apps.spec.ts    # Embedded app views and URL rewriting
  app-functionality.spec.ts # Per-app login and basic functionality
  uninstall.spec.ts        # App uninstallation
```

## Debugging Failed Tests

Each test captures:
- Console logs
- Network requests/responses
- Screenshots on failure
- Video on retry

Find artifacts in `test-results/` and HTML report via:

```bash
npm run report
```

## Test Coverage

### User Apps Tested

- **miniflux** - RSS reader (supports BASE_URL, native OIDC SSO)
- **actual-budget** - Budgeting app (requires URL rewriting)
- **adguard-home** - DNS ad-blocker (requires URL rewriting)
- **jellyfin** - Media server (requires URL rewriting, LDAP SSO)
- **qbittorrent** - Torrent client (requires URL rewriting, forward-auth SSO)
- **affine** - Knowledge base (requires URL rewriting, native OIDC SSO)
- **radarr** - Movie manager (requires URL rewriting, forward-auth SSO)
- **sonarr** - TV show manager (requires URL rewriting, forward-auth SSO)
- **prowlarr** - Indexer manager (requires URL rewriting, forward-auth SSO)

### Key User Flows

1. **Catalog Browsing** - View apps, search, filter by category
2. **App Installation** - Install from catalog, handle integration choices
3. **App Launching** - Click to open in iframe viewer
4. **Embedded App Navigation** - URL rewriting for apps without BASE_URL support
5. **App Functionality** - Login forms, basic UI interactions
6. **App Uninstallation** - Context menu uninstall, confirmation modal
7. **Real-time Updates** - SSE-driven UI updates
