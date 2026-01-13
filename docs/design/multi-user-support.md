# Multi-User Support for Bloud

## Overview

Add user identity tracking for family homelab sharing. Users are queried directly from Authentik - no local caching needed.

**Scope:** Phase 1 only (user foundation). Future phases documented below.

---

## Phase 1: User Foundation (THIS IMPLEMENTATION)

**Goal:** Expose user information from Authentik via API.

### Design Decision: Query Authentik Directly

No local database table needed. Authentik is always running and is the source of truth.

- `GET /api/users` → Query Authentik API directly
- `GET /api/users/me` → Read from request headers (set by forward-auth)

### Implementation

1. **Extend Authentik client** (`internal/authentik/client.go`)
   - `ListUsers() ([]UserResponse, error)` - fetch from `/api/v3/core/users/`

2. **Add API endpoints** (`internal/api/routes.go`)
   - `GET /api/users` - Query Authentik, return all users
   - `GET /api/users/me` - Return current user from `X-authentik-*` headers

### Files Modified

| Action | File |
|--------|------|
| Modify | `internal/authentik/client.go` |
| Modify | `internal/authentik/interfaces.go` |
| Modify | `internal/api/routes.go` |
| Modify | `internal/api/server.go` |

### Verification

1. Start dev environment: `./bloud start`
2. Create test users in Authentik UI (`localhost:8080/if/admin`)
3. List users: `curl localhost:3000/api/users`
4. Test current user (when logged in via SSO): `curl localhost:3000/api/users/me`

---

## Future Phases (Not Implemented Now)

### Phase 2: Identity Propagation
- Add `groups` scope to OIDC blueprints
- Add `userAware` field to app metadata

### Phase 3: User Provisioning
- Pre-create users in apps (Jellyfin, Jellyseerr)
- Sync group-based permissions

### Phase 4: Per-User App Access
- Local database table for access controls (this is when we'd need local storage)
- Filter app listing by user permissions

---

## Design Decisions

1. **No local caching** - Query Authentik directly, it's always running
2. **Headers for current user** - Forward-auth already sets `X-authentik-*` headers
3. **Add local storage later** - Only when we need to store something Authentik doesn't have
