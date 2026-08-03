# PR #34 Handoff: Deep Modules Server Refactor

**Branch:** `refactor/server-deep-modules` (at commit `6bac32d`)
**Original PR Title:** refactor(server): deep modules — auth, apps, home, logs, remote_apps, settings, sharing, system

---

## What Was Done

### 1. Module Files Created (8 modules, all compile)

Each module has a stable interface, concrete implementation, HTTP handler methods, and a router factory:

| Module | File | Key Interface Methods |
|--------|------|----------------------|
| **AppsModule** | `apps_module.go` | RefreshCatalog, GetCatalog, GetInstalled, AppMetadata, Install, Uninstall, Rename, ClearData |
| **AuthModule** | `auth_module.go` | LoginHandler, CallbackHandler, LogoutHandler, GetCurrentUserHandler |
| **HomeModule** | `home_module.go` | GetLayout, SetLayout |
| **LogsModule** | `logs_module.go` | CanStream + 2 SSE handlers |
| **RemoteAppsModule** | `remote_apps_module.go` | List, Add, Delete |
| **SettingsModule** | `settings_module.go` | Tailnet CRUD, Setup wizard, User CRUD + Role |
| **SharingModule** | `sharing_module.go` | CommunityGraph, CreateInvite, ListShares, RevokeShare, ListGuests, CreateGuest |
| **SystemModule** | `system_module.go` | Health, SystemStatus, Storage, DeveloperGraph |

### 2. Shared Infrastructure

- **`router.go`** — `NewRouter(db, cfg, logger) *chi.Mux` creates all modules, wires middleware (CORS, logging, recovery, timeouts), and assembles the route tree. Also contains: `authMiddlewareFn`, `adminMiddlewareFn`, `respondJSON`, `respondError`, `initOrchestratorHelper`, `initAuthHelper`, `refreshCatalogHelper`, `deriveSecretHelper`, `rebuildStreamHandler`, `setupFrontendHelper`.
- **`server.go`** — Slim `Server` struct with `NewServer`, `Start`, `Shutdown`, `OrchestratorReady`, `CheckSystemHealth`. Delegates all initialization to `NewRouter`.
- **`auth.go`** — Constants (`sessionCookieName`, `stateCookieName`, `stateCookieMaxAge`), types (`AuthConfig`, `contextKey`, `userContextKey`), and helpers (`isLocalRequest`, `getUserFromContext`, `requestBaseURL`, `generateState`).

### 3. Shared Types Moved Into Module Files

| Type | Moved To |
|------|----------|
| `installedAppResponse`, `enrichApps` | `apps_module.go` |
| `appWithPosition`, `widgetPosition`, `homeResponse` | `home_module.go` |
| `graphNode`, `graphEdge`, `developerGraph` | `system_module.go` |
| `createInviteRequest`, `createInviteResponse`, `communityGraphResponse`, `communityNode`, `communityEdge`, `createGuestRequest` | `sharing_module.go` |
| `tailnetResponse`, `toTailnetResponse`, `setTailnetRequest`, `SetupStatusResponse`, `CreateUserRequest`, `CreateUserResponse`, `createUserRequest`, `setUserRoleRequest`, `validateCreateUserRequest`, `validationError` | `settings_module.go` |

### 4. Old Handler Files Deleted

`routes.go`, `home.go`, `logs.go`, `remote_apps.go`, `settings.go`, `setup.go`, `sharing.go`, `users.go`, `sse.go`, `developer.go`

### 5. Build Status

- **Production code compiles:** ✅ `go build ./...` passes (all packages)
- **Module unit tests pass:** ✅ `apps_module_test`, `auth_module_test`, `home_module_test`, `logs_module_test`, `remote_apps_module_test`, `settings_module_test`, `sharing_module_test`, `system_module_test`
- **Other package tests pass:** ✅ All non-api tests pass
- **api_test.go FAILS:** ❌ Needs rewrite (see remaining work #1)

---

## Remaining Work (Picked Up by Next Agent)

### 🔴 Priority 1: Fix api_test.go

`setupTestServer` was updated to return `(*chi.Mux, string)` but ~30 test functions still use the old `server.router.ServeHTTP(...)` pattern.

**Changes needed in `api_test.go`:**
1. Replace all `server.router.ServeHTTP(w, req)` → `server.ServeHTTP(w, req)` (since `server` is now `*chi.Mux`)
2. Remove dead references: `server.orch = nil`, `server.appStore.(*FakeAppStore)`, `server.sessionStore = &store.SessionStore{}`, `server.cfg.DataDir = dataDir`
3. Remove calls to `server.setupMiddleware()`, `server.setupRoutes()`, `server.router = chi.NewRouter()`
4. For tests that need fake app store injection (e.g., `TestAPI_ClearData_InstalledApp`), either:
   - Add a `FakeAppStore` that implements `store.AppStoreInterface` and pass it through a test-friendly constructor
   - Or restructure those tests to work with the real stores + real catalog loader
5. For auth middleware tests (`TestAuthMiddleware_NoSession`, `TestAPI_PublicEndpoints_NoAuthRequired`), need a way to inject `sessionStore`. Consider:
   - A `NewTestRouter(db, cfg, logger, opts TestOpts)` function that accepts optional fakes
   - Or set `cfg.RedisAddr = ""` and test without session auth
6. Remove unused imports: `orchestrator`, `store` (if no longer needed), `context` (if unused)

### 🔴 Priority 2: Fix Route Wiring in NewRouter

**Critical bug:** Module routers define paths WITH `/api` prefix (e.g., `r.Route("/api", func(api chi.Router) { api.Get("/apps", ...) })`). But `NewRouter` mounts them inside the main `/api` group via `Mount("/", ...)`, causing double prefix: `/api/api/apps`.

**Fix options (pick one):**

**Option A — Strip `/api` from module routers (cleaner):**
- Edit every `NewXxxRouter` function to remove the `r.Route("/api", ...)` wrapper
- Example for apps: change `r.Route("/api", func(api chi.Router) { api.Get("/apps", ...) })` → `r.Get("/apps", ...)` directly on the root router
- Same for all 8 module routers

**Option B — Mount at root level (less invasive):**
- In `NewRouter`, don't mount inside `/api` group
- Mount each module router directly on `r` (the root router)
- Apply auth middleware to specific sub-routers instead of a group
- This means restructuring the middleware application

**Recommended: Option A.** It's a targeted change in each `*_module.go` router factory.

### 🟡 Priority 3: Update main.go for New Server Contract

`main.go` calls:
```go
server := api.NewServer(database, api.ServerConfig{...}, logger)
server.OrchestratorReady()  // now returns immediately-closed channel
server.CheckSystemHealth()  // now returns nil
server.Start()
server.Shutdown(shutdownCtx)
```

- `OrchestratorReady()` and `CheckSystemHealth()` are currently no-ops. Decide if you want to:
  - Restore real behavior (need to pass orchestrator through to Server)
  - Keep as no-ops (simpler, health checked via `/health` endpoint)
- If restoring: Server needs `orch *orchestrator.Orchestrator` field populated by `NewRouter`

### 🟡 Priority 4: Verify Integration

After fixing #1 and #2:
1. Run `go test ./...` — all tests should pass
2. Run `go build ./cmd/host-agent/` — binary should build
3. Manually verify route paths are correct (no double `/api/api/...`)
4. Consider adding a smoke test that hits `/api/health`, `/api/apps`, `/auth/login` to verify the full stack

---

## Architecture Reference

```
router.go (NewRouter)
  ├── Creates stores, catalog, orchestrator, auth config
  ├── Creates 8 module instances with injected dependencies
  ├── Applies middleware: RequestID → RealIP → Logger → Recoverer → Timeout → CORS
  ├── Public routes: /health, /auth/login, /auth/callback, /auth/logout
  ├── /api group:
  │   ├── Public: /health, /setup/status, /auth/me
  │   └── Auth group (authMiddleware):
  │       ├── User routes: apps, home, logs
  │       └── Admin group (adminMiddleware):
  │           ├── Apps admin (refresh-catalog)
  │           ├── Settings (tailnet, setup, users)
  │           ├── Sharing
  │           └── Remote apps
  └── Frontend catch-all + 404

server.go (NewServer)
  └── Calls NewRouter, returns Server{router, db, logger}
  └── Start/Shutdown/OrchestratorReady/CheckSystemHealth

auth.go
  └── Constants, AuthConfig, helpers (isLocalRequest, getUserFromContext, etc.)
```

## Key Patterns

- **Module Interface** hides implementation (e.g., `AppsModule` interface)
- **Dependency Injection** via `NewXxxModule(...)` constructors
- **Router Factory** pattern: `NewXxxRouter(*xxxModule) *chi.Mux`
- **Handler Pattern**: Methods on concrete type returning `http.HandlerFunc`
- **Middleware Functions**: Standalone `authMiddlewareFn` / `adminMiddlewareFn` (not methods on Server)
- **Orchestrator abstraction**: Modules use `orchestratorCaller` interface, not `*orchestrator.Orchestrator` directly

## Files to Edit for Remaining Work

| File | Action |
|------|--------|
| `router.go` | Fix route wiring (strip double `/api` prefix) |
| `apps_module.go` | Fix `NewAppsRouter` path prefix |
| `auth_module.go` | Fix `NewAuthRouter` path prefix |
| `home_module.go` | Fix `NewHomeRouter` path prefix |
| `logs_module.go` | Fix `NewLogsRouter` path prefix |
| `remote_apps_module.go` | Fix `NewRemoteAppsRouter` path prefix |
| `settings_module.go` | Fix `NewSettingsRouter` path prefix |
| `sharing_module.go` | Fix `NewSharingRouter` path prefix |
| `system_module.go` | Fix `NewSystemRouter` path prefix |
| `api_test.go` | Rewrite for new test helper signature |
| `server.go` | (Optional) Restore real OrchestratorReady/CheckSystemHealth |
| `main.go` | (Optional) Verify server contract matches |
