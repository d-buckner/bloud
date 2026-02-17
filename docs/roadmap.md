# Bloud Roadmap

Future features and improvements planned for Bloud.

---

## Phase 0: Prototype (Complete)

#### Scalable OAuth Callback Pattern
- **Issue identified**: Actual Budget's native OIDC approach used root-level Traefik routes (`/openid`), violating the "no app-specific root routes" architecture
- **Solution**: Service worker now handles OAuth callback rewriting
  - Detects OAuth callback patterns (`/openid/callback`, `/oauth2/callback`, etc.)
  - Uses `lastKnownAppContext` to route callbacks to correct embedded app
  - Rewrites `/openid/callback` → `/embed/{appName}/openid/callback`
- **Result**: Removed `apps/actual-budget/traefik-routes.yml` - no more app-specific root routes
- See `docs/design/sso-appviewer-integration.md` for full OAuth flow documentation

#### Core Infrastructure
- NixOS + rootless Podman working
- Shared PostgreSQL for all apps
- Shared Redis for all apps (Authentik, future apps)
- Authentik SSO (forward auth + OIDC patterns)
- Traefik reverse proxy with dynamic routing
- `mkBloudApp` helper for standardized app modules
- Integration graph with dependency resolution and install planning
- 14 apps implemented: Postgres, Redis, Traefik, Authentik, Miniflux, Actual Budget, AdGuard Home, qBittorrent, Jellyfin, Jellyseerr, Affine, Prowlarr, Radarr, Sonarr

#### Secrets Management
- Go-based secrets manager auto-generates cryptographic secrets on first boot
- Secrets stored in `secrets.json` (0600 permissions), env files generated per-service
- Covers: PostgreSQL password, Authentik keys, LDAP credentials, SSO host secret, per-app OAuth/admin secrets
- Bootstrap credentials (`admin123`) only used as throwaway values during initial app setup, deleted after LDAP configuration

#### App Configurator System
- Prestart/poststart hooks wired through systemd → host-agent → per-app `configurator.go`
- Configurators handle: Authentik API token creation (via Django shell), LDAP setup, app-specific bootstrap
- Idempotent — safe to re-run on every service start

#### API Authentication
- OIDC login flow via Authentik with session management
- All `/api/*` endpoints require valid session (except `/api/health`, `/api/setup/*`, `/api/auth/me`)
- Sessions stored in Redis

#### Install Queue
- Operation queue with 5-second batching window for concurrent install requests
- Deduplication merges multiple requests for the same app
- Prevents `apps.nix` corruption and nixos-rebuild conflicts

#### SSO Implementation Patterns
1. **Forward Auth + Headers** (preferred for apps that support it)
   - Apps: Miniflux, Gitea, Grafana, Paperless
   - Single Traefik middleware for all `/embed/*`
   - No per-app configuration needed

2. **Native OIDC via Service Worker** (for apps requiring OIDC)
   - Apps: Actual Budget, Immich, Vaultwarden
   - Register root-level redirect_uri in Authentik (for OAuth validation)
   - SW rewrites callback to `/embed/{app}/` path
   - No root-level Traefik routes needed

#### ISO Build Pipeline
- GitHub Actions builds Go binary and frontend outside Nix sandbox
- NixOS ISO packages pre-built artifacts (no vendorHash/npmDepsHash fragility)
- First-boot `bloud-init-secrets` service generates secrets before apps start
- Port 80 → 8080 NAT redirection for rootless Traefik

---

## Phase 1: Alpha (Current)

### Security Hardening

#### Disk Encryption (LUKS)
- **Problem**: Secrets are properly permissioned (0600) but stored as plaintext on disk. Protects against running-system access but not physical disk theft or image cloning.
- **Solution**: LUKS full-disk encryption on the ISO image
  - TPM2 auto-unlock for headless boot (no passphrase needed)
  - Protects all data at rest — secrets, databases, app volumes, everything
  - NixOS has built-in support via `boot.initrd.luks.devices`
- **Why not sops-nix**: Secrets are generated at runtime (never in git), and the decryption key would sit on the same disk. LUKS protects the entire disk with hardware-backed keys.
- **Why not systemd-creds**: Only protects individual credential files, requires re-encryption on every secret change, and secrets still end up in container environments at runtime. LUKS is simpler and more comprehensive.

#### Container Security
- Audit container network isolation
- Review volume mount permissions
- Ensure no containers run as privileged

### Frontend Improvements

#### iFrame State Preservation
- When switching tabs, maintain old iframe elements (hidden) to preserve app state
- Only dispose iframe when tab is explicitly closed
- Prevents apps from losing unsaved state on tab switch

#### Client-Side Storage Isolation
- **Problem**: Embedded apps share the same origin, so they can read/overwrite each other's client-side storage
- **Affected storage types**:
  - **LocalStorage**: Persists across sessions, shared by all apps
  - **SessionStorage**: Per-tab, but apps can conflict when switching between apps in the same Bloud tab
  - **Cookies**: Shared across all apps, also sent with HTTP requests
  - **IndexedDB**: Already handled via intercept injection (see `service-worker/inject.ts`)
- **Solution approach**: Namespace isolation via service worker injection
  - Inject script that patches `localStorage.getItem/setItem`, `sessionStorage.getItem/setItem`
  - Transparently prefix keys with app name (e.g., `actual-budget:theme`)
  - App reads/writes `theme`, actually stored as `actual-budget:theme`
  - Prevents cross-app conflicts without app modifications
- **Cookies**: TBD - more complex due to server-side interaction
  - Options: `document.cookie` interception, path-based isolation, or accept shared cookies
- **Related**: IndexedDB intercept system (`bootstrap.indexedDB.intercepts` in metadata.yaml)

### Testing Infrastructure

#### End-to-End Tests
- Add Playwright tests for critical flows:
  - App installation
  - SSO login flow
  - Embedded app navigation
  - OAuth callback handling

#### Integration Test Expansion
- Test app dependency resolution end-to-end
- Test Traefik config generation with various app combinations
- Test database initialization and migrations

### App Catalog Expansion
Target: 10 core apps for alpha release (exceeded - 14 implemented)
- [x] Postgres (infrastructure)
- [x] Traefik (infrastructure)
- [x] Authentik (SSO)
- [x] Miniflux (RSS reader)
- [x] Actual Budget (finance)
- [x] AdGuard Home (DNS)
- [x] Redis (infrastructure)
- [x] Jellyfin (media server)
- [x] Jellyseerr (media requests)
- [x] qBittorrent (downloads)
- [x] Prowlarr (indexer manager)
- [x] Radarr (movie management)
- [x] Sonarr (TV management)
- [x] Affine (notes/whiteboard)
- [ ] Immich (photos)
- [ ] Nextcloud or alternative (files)

---

## Phase 2: Beta

### Multi-Host Orchestration

**This is Bloud's key differentiator** - no competitor offers this.

#### Architecture
- USB drive = OS (portable between machines)
- System state = local storage
- Web UI shows all hosts and app distribution
- Drag-and-drop app migration between hosts

#### Implementation Steps
1. **Host discovery** - mDNS/DNS-SD for local network discovery
2. **Host registration** - Central registry of known hosts
3. **State synchronization** - Distributed state for app placement
4. **App migration** - Volume backup → transfer → restore flow
5. **DNS/routing** - Update Traefik config across hosts

#### Challenges
- Data migration for stateful apps (databases, media)
- Network partition handling
- Conflict resolution for concurrent changes

---

## Phase 3: 1.0 Release

### Auto-Update System
- Automatic NixOS configuration updates
- Staged rollouts with canary testing
- Automatic rollback on failure (NixOS atomic)
- User-controlled update schedule

### Mobile App
- Monitoring: host status, app health, resource usage
- Notifications: alerts, backup status, updates
- Basic management: start/stop apps, view logs

### App Ecosystem
- Community app submission process
- App review/approval workflow
- Versioned app definitions
- App update notifications

### Documentation
- User guide (getting started, app installation)
- App contributor guide (how to add apps)
- Architecture documentation
- API reference

---

## Backlog (Unprioritized)

### Performance
- Profile app startup times
- Optimize Traefik config generation
- Consider lazy-loading for large app catalogs
- Database query optimization for host-agent
- **App install performance logging** - Instrument the install flow to track total install time and per-step timing (dependency resolution, NixOS rebuild, container pull, configurator execution, health checks). Surface this in logs and potentially in UI for debugging slow installs.

### Developer Experience
- Hot reload improvements for NixOS modules
- Better error messages in host-agent logs
- Debug mode for service worker
- App development scaffolding tool

### Observability
- Centralized logging (Loki or similar)
- Metrics collection (Prometheus)
- Health dashboard in shell
- Alerting rules for common issues

### Advanced Features
- Scheduled tasks (cron-like)
- Notification system (email, push, webhook)
- Theme customization for shell
- Keyboard shortcuts in UI

---

## Technical Debt

### Medium Priority
- [ ] Multi-service app metadata - Apps should declare all services (not just one) so orchestrator can properly track health and dependencies (e.g., qbittorrent runs both `flood` and `qbittorrent` containers)
- [ ] Defensive error handling in graph.go/plan.go
- [ ] Test coverage for edge cases

### Low Priority
- [ ] Code comments for complex logic
- [ ] Consistent error types in Go code
- [ ] Frontend component documentation

---

## Notes

### Architecture Constraints (Do Not Violate)
1. **No app-specific routes at root level** - All apps under `/embed/{appName}/`
2. **Shared infrastructure** - Max one PostgreSQL/Redis/Authentik per host
3. **Rootless containers** - No privileged Podman operations
4. **Atomic updates** - NixOS rollback must always work
5. **Same-origin embedding** - All apps served from same origin (enables SW control, requires storage isolation)

### Decision Log
- **2026-02**: LUKS over sops-nix for secrets-at-rest — secrets are runtime-generated (never in git), LUKS protects entire disk with TPM2 auto-unlock
- **2026-02**: External host URLs split into browser-facing and server-side Authentik endpoints
- **2026-02**: Pre-built artifacts in ISO (Go/npm built outside Nix sandbox, eliminates vendorHash churn)
- **2026-01**: IndexedDB intercept injection via SW (solves app init overwriting configured values)
- **2026-01**: Service worker for OAuth callbacks (maintains routing architecture)
- **2026-01**: Service worker routing stabilized (explicit app context, proper state management)
- **2026-01**: Migrated host-agent from SQLite to PostgreSQL (shared infrastructure pattern)
- **2026-01**: Frontend state management refactored (api/, services/, stores/ separation; no data duplication)
