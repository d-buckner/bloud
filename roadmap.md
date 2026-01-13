# Bloud Roadmap

Future features and improvements planned for Bloud.

---

## Phase 0: Prototype (Current)


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
- Authentik SSO (forward auth + OIDC patterns)
- Traefik reverse proxy with dynamic routing
- `mkBloudApp` helper for standardized app modules
- Integration graph with dependency resolution and install planning
- 7 apps working: Postgres, Traefik, Authentik, Miniflux, Actual Budget, AdGuard Home, Redis

### In Progress

#### SSO Coverage Expansion
- **Miniflux**: Supports header auth (`AUTH_PROXY_HEADER`). Use forward auth + header injection. (Working)
- **AdGuard Home**: Research SSO options. May need native OIDC (use same SW pattern as Actual Budget).

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

---

## Phase 1: Alpha

### Security Hardening (Pre-Alpha Blocker)

#### Secrets Management
- **Problem**: Hardcoded credentials throughout codebase (`admin123`, `testpass123`)
- **Solution**: Integrate sops-nix or agenix for encrypted secrets
- **Scope**:
  - PostgreSQL credentials
  - Authentik admin/API keys
  - Per-app secrets (OAuth client secrets, API keys)
  - User-configurable secrets via UI

#### API Authentication
- **Problem**: Host-agent API endpoints appear to lack authentication
- **Solution**:
  - Require Authentik session for all `/api/*` endpoints
  - Add CSRF protection for state-changing operations
  - Consider API rate limiting

#### Container Security
- Audit container network isolation
- Review volume mount permissions
- Ensure no containers run as privileged

### Frontend Improvements

#### iFrame State Preservation
- When switching tabs, maintain old iframe elements (hidden) to preserve app state
- Only dispose iframe when tab is explicitly closed
- Prevents apps from losing unsaved state on tab switch

#### Unified State Management
- Current: Multiple stores (`apps`, `openApps`, `appActions`)
- Consider consolidating into single store with slices
- Add proper loading/error states throughout UI

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
Target: 10 core apps for alpha release
- [x] Postgres (infrastructure)
- [x] Traefik (infrastructure)
- [x] Authentik (SSO)
- [x] Miniflux (RSS reader)
- [x] Actual Budget (finance)
- [x] AdGuard Home (DNS)
- [x] Redis (infrastructure)
- [ ] Immich (photos)
- [ ] Jellyfin (media server)
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

### App Configurator System

#### Current State
- `configure.go` files exist in app directories
- Infrastructure for configurators exists but isn't wired up

#### Needed
1. **Execution pipeline** - Run configurators after app install
2. **Integration API** - Standard interface for:
   - Authentik OAuth2 provider creation
   - Inter-app configuration (Sonarr → qBittorrent)
   - Traefik route registration
3. **Idempotency** - Configurators must be safe to re-run
4. **Error handling** - Rollback on configuration failure

### Shared Redis Consolidation
- Currently Authentik runs its own Redis instance
- Consolidate to single shared Redis per host
- Update Authentik config to use shared instance
- Benefits: Lower memory usage, simpler operations

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

### High Priority
- [ ] Secrets management (blocking for production)
- [ ] API authentication (security)

### Medium Priority
- [x] ~~Switch host-agent from SQLite to system PostgreSQL~~ (Done)
- [x] ~~Frontend state management consolidation~~ (Done)
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

### Decision Log
- **2026-01**: Service worker for OAuth callbacks (maintains routing architecture)
- **2026-01**: Service worker routing stabilized (explicit app context, proper state management)
- **2026-01**: Migrated host-agent from SQLite to PostgreSQL (shared infrastructure pattern)
- **2026-01**: Frontend state management refactored (api/, services/, stores/ separation; no data duplication)
