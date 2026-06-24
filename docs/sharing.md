# Bloud Sharing

**Status:** Proposed  
**Last updated:** 2026-06-21

---

## Overview

Bloud sharing makes self-hosting federated. A server owner can invite another Bloud user
to access a specific app on their server. The invited user's Bloud instance proxies that
app locally, so dumb clients (TVs, game consoles, etc.) can reach it through a familiar
local endpoint — as if the app were installed on the guest's own machine.

The network layer is built on [Tailscale](https://tailscale.com/) or a self-hosted
[Headscale](https://github.com/juanfont/headscale) instance, providing encrypted
peer-to-peer tunnels without port forwarding or exposing services to the public internet.

---

## Core Principles

- **Per-app granularity.** Sharing is scoped to a single app, not a whole server. The
  owner shares Jellyfin without also sharing Radarr or Sonarr.
- **Direct invites only.** An owner invites a specific person. Guests cannot re-share
  apps they've been given access to. This gives the owner strong assurance over who has
  access.
- **Local proxy for dumb clients.** The guest's Bloud instance proxies the remote app
  through a local endpoint. Devices that cannot join a tailnet (TVs, set-top boxes,
  consoles) connect to the guest's local address and get transparent access.
- **Smart clients connect directly.** Browsers and capable apps can connect directly to
  the app over the tailnet, bypassing the proxy for lower latency.
- **Revocable at any time.** The owner can revoke access for a specific user, immediately
  removing their tailnet access.

---

## Network Architecture

Every non-system user app gets a dedicated Tailscale sidecar container that starts and
stops with the app. The sidecar is always running whenever the app is running — it is
not created or removed when shares are created or revoked. This sidecar:

- Joins the tailnet as its own node (e.g. `ts-navidrome`)
- Uses Tailscale Serve to proxy incoming tailnet connections to the app's container port
- Is the only entry point into the app over the tailnet

Sharing = granting a guest the sidecar's tailnet address (in the invite token) and an
authorization binding to use it. Revoking = marking the share revoked so the proxy returns 403.
The sidecar itself is unaffected by share lifecycle. No other apps are reachable through
a given app's sidecar.

```
┌─────────────────────────────────────────────────┐
│ Alice's Bloud Host                               │
│                                                  │
│  apps-net:                                       │
│  ┌────────────┐     ┌──────────────────────┐     │
│  │ navidrome  │◄────│    ts-navidrome       │─────┼── tailnet
│  │   :4533    │     │  (TS Serve → :4533)  │     │
│  └────────────┘     └──────────────────────┘     │
└──────────────────────────────────────────────────┘
                                │
                          WireGuard (P2P)
                                │
┌───────────────────────────────┼──────────────────┐
│ Bob's Bloud Host              │                  │
│                               │                  │
│  host-agent (tsnet) ──────────┘                  │
│       │                                          │
│  /embed/navidrome@alice-server/rest/* ──► proxy  │
│       ▲                                          │
└───────┼──────────────────────────────────────────┘
        │
  Bob's Subsonic client / TV / phone
```

The guest's host-agent uses a single `tsnet` node for **outbound dialing only** — it has
no listening ports on the tailnet. This is simpler than per-app sidecars on the guest side
while still providing the full per-app isolation guarantee on the host side.

### Headscale / Tailscale Configuration

Bloud does not prescribe which control plane is used. The owner configures one of:

- **Tailscale** (commercial, easiest) — authenticate with a Tailscale auth key
- **Self-hosted Headscale** — run your own Headscale instance on a VPS
- **Community Headscale** — a community member runs a Headscale instance for a group

In all cases, Bloud only needs a `TS_AUTHKEY` (reusable). The control plane choice is an
operator concern. Headscale's control plane sees node registrations and coordinates key
exchange, but all data traffic is direct peer-to-peer WireGuard — the control plane
operator cannot read app traffic.

---

## User Identity and Downstream App Accounts

The invite token authorizes creation of a Bloud user on the owner's instance. It does not
ask the guest for app-local credentials and does not carry downstream passwords.

After redemption, Bloud treats the guest as a normal user on the host instance and maps that
Bloud identity into each shared app according to the app's declared authentication
capability:

| Capability | Share behavior |
|---|---|
| Native OIDC/SAML | Guest authenticates with Bloud/Authentik and the app receives a normal SSO login. |
| Trusted header auth | Authentik forward auth protects the route; the proxy injects a mapped identity header that the app explicitly trusts. |
| LDAP | Bloud/Authentik exposes the user through the LDAP integration and provisions required groups. |
| App admin API only | Bloud creates an app-local user using a random high-entropy secret the guest does not know. |
| No external auth/provisioning API | Bloud can gate network access, but the app-local login remains a degraded manual integration. |

Bloud should avoid using user-supplied app passwords as a provisioning primitive. If an app
leaves no alternative, Bloud may generate a one-time app password, show it once, and mark
the integration as degraded.

---

## Forward-Auth Apps and Native Client Sharing

Forward auth and header auth are separate capabilities.

Forward auth means Traefik asks Authentik whether a request may pass. Header auth means the
upstream app explicitly trusts a proxy-supplied identity header as the logged-in user. A
forward-auth app is not automatically logged in as a mapped user unless it also supports
trusted header authentication.

Navidrome is the canonical example. Its web UI can be protected by Authentik forward auth
and Navidrome can treat a trusted header such as `Remote-User` as the application user,
provided the request comes from a configured trusted proxy. Bloud maps:

```text
Bloud user -> Authentik session -> proxy identity header -> Navidrome user
```

The proxy must strip inbound identity headers from the client before authentication and
inject only Bloud-controlled values afterward. The app must be reachable only through that
trusted proxy path.

Some apps also serve native protocol clients through a separate API path with its own
credential scheme. Navidrome's Subsonic API at `/rest/` is the common case. When a protocol
cannot delegate authentication to Bloud/Authentik, Bloud declares that path as a bypass route
and treats it as a separate native-client contract.

### What Gets Shared

For trusted-header forward-auth apps, sharing can target the browser web UI:

- The guest signs in to the host Bloud instance through the invite-created account
- Authentik forward auth validates the session
- The proxy injects the app's configured identity header
- The app uses the mapped app-local user

For native client paths that cannot use SSO, sharing targets the **native client path**:

- The guest's Bloud proxy exposes the bypass path (e.g. `/rest/`) via the tailnet
- The guest configures their native client (Subsonic app, RSS reader, etc.) to point at
  the guest's local Bloud proxy address
- The proxy forwards requests over the tailnet to the host's bypass route
- If the native protocol requires app-local credentials, the app integration must define
  how those credentials are provisioned, exposed, rotated, and revoked

### Revocation for Forward-Auth Apps

Revocation marks the share revoked and removes the guest's authorization to the shared app.
The sidecar keeps running with the app. Bloud should also reconcile downstream state:

- Remove or disable the app-local user where the app API supports it
- Remove Authentik group membership or application authorization
- Revoke generated native-client secrets where possible
- Record degraded cleanup when the app has no revocation API

---

## Invite Flow

```
1. OWNER CREATES INVITE
   Owner clicks Share in Bloud UI.
   → Sidecar is already running (started with the app) — no wait
   → Bloud reads the sidecar's tailnet address (podman exec ts-{appName} tailscale ip)
   → Bloud generates a signed invite token containing:
       - shareId
       - appId, appName, hostLabel
       - ssoStrategy, bypassPaths
       - identity/provisioning strategy
       - sidecarTailnetAddr (the app's permanent tailnet address)
       - expiry (1 hour)
   → Token displayed as a string for copy/paste
   Owner shares the token with the guest out of band (message, etc.)

2. GUEST ACCEPTS
   Guest pastes the token into their Bloud UI.
   Guest follows the host Bloud redemption URL or the guest instance brokers the redemption.
   Host Bloud validates signature, expiry, intended app, and consumed state.
   Guest creates or binds a Bloud user on the host instance.
   Host Bloud records the share-user binding and provisions downstream app identity.

3. GUEST BLOUD UI
   Dashboard shows the app with a "Hosted by Alice's Server" badge.
   Browser access uses the host Bloud/Authentik session and mapped downstream identity.
   Native clients use the declared native-client contract for the app.
```

---

## Proxy Behavior

### All Clients (Default Path)

The guest's Bloud exposes the remote app at a local path:

```
localhost:8080/embed/navidrome@alice-server/
```

Requests to this path are reverse-proxied over the tailnet to the host's sidecar. Browser
requests rely on Bloud/Authentik authentication at the host and app-specific identity
mapping. Native-client requests use the app's declared native-client contract.
This works for any client, including dumb clients that cannot join a tailnet themselves.

For apps with native-client bypass paths, the proxy routes only to declared paths unless
the app also supports browser sharing through trusted header auth or native SSO.

### Smart Clients (Direct Path)

For clients capable of connecting directly to the tailnet, the guest can retrieve the
sidecar's tailnet address from the Bloud UI and configure the client to connect directly.
This avoids the proxy hop and reduces latency — relevant for high-bitrate video.

### Offline / Host Unreachable

If the host is unreachable over the tailnet, the proxy returns a clear error. No content
is cached. The guest's Bloud UI marks the app as offline.

---

## Revocation

**Owner-initiated:**
- Share record marked revoked
- Downstream authorization and app-local users/secrets are disabled where supported
- Guest discovers revocation on next proxy request (returns 403)
- Sidecar keeps running — it is tied to app lifecycle, not share lifecycle

**Guest-initiated:**
- Guest removes the shared app from their Bloud UI
- RemoteApp row deleted from guest DB
- No notification to host; share record on host remains active until owner revokes

**Node expiry:**
If the tailnet connection drops, the proxy fails and the UI shows the app as offline.
The owner can re-invite to restore access.

---

## Security Properties

| Property | Mechanism |
|---|---|
| Only invited users reach the app | Per-app sidecar — each shared app has its own tailnet node; no other apps are reachable through it |
| Traffic is encrypted in transit | WireGuard (Tailscale/Headscale) end-to-end |
| Control plane can't read traffic | P2P WireGuard; Headscale operator sees node list only |
| Invites do not expose app passwords | Token creates or binds a Bloud user; downstream users are provisioned by adapters |
| Header auth is not forgeable by clients | Proxy strips inbound identity headers and injects trusted headers only after auth |
| Invites are single-use and time-limited | HMAC-signed token, 1-hour TTL, server-side consumed flag |
| No transitive sharing | Guest cannot produce invite tokens for apps they don't own |
| Revocation is immediate | Share marked revoked — proxy returns 403 on next request |

---

## Data Model

### Host side — `shares` table

```sql
CREATE TABLE IF NOT EXISTS shares (
    id           TEXT PRIMARY KEY,
    app_id       TEXT NOT NULL,
    sso_strategy TEXT NOT NULL DEFAULT 'native-oidc',  -- native-oidc | forward-auth
    guest_label  TEXT NOT NULL,                         -- display name set by owner
    status       TEXT NOT NULL DEFAULT 'active',        -- active | revoked
    created_at   TEXT DEFAULT (datetime('now')),
    revoked_at   TEXT
);
```

Note: status starts as `active` because the sidecar is always already running when the
invite is created.

### Guest side — `remote_apps` table

```sql
CREATE TABLE IF NOT EXISTS remote_apps (
    id                   TEXT PRIMARY KEY,   -- shareId from invite token
    host_label           TEXT NOT NULL,
    app_id               TEXT NOT NULL,
    app_name             TEXT NOT NULL,
    sso_strategy         TEXT NOT NULL,
    bypass_paths         TEXT NOT NULL DEFAULT '[]',  -- JSON array
    sidecar_tailnet_addr TEXT NOT NULL,
    remote_user_id       TEXT,
    status               TEXT NOT NULL DEFAULT 'pending_redemption',
    created_at           TEXT DEFAULT (datetime('now'))
);
```

---

## MVP Scope

In scope:

- [ ] Per-app Tailscale sidecar started/stopped with every non-system app (orchestrator)
- [ ] DB schema: `shares` and `remote_apps` tables
- [ ] Signed invite token generation (stdlib HMAC-SHA256, no external JWT dep)
- [ ] `POST /api/sharing/invites` — read sidecar tailnet addr, generate token, create share
- [ ] `GET /api/sharing/shares` — list active shares (owner view)
- [ ] `DELETE /api/sharing/shares/{id}` — mark revoked (sidecar unaffected)
- [ ] `POST /api/remote-apps/accept` — redeem token and create/bind host Bloud user
- [ ] `GET /api/remote-apps` — list remote apps (guest dashboard)
- [ ] `DELETE /api/remote-apps/{id}` — guest removes (local only)
- [ ] `tsnet` node in guest host-agent for outbound dialing
- [ ] `/embed/{appId}@{hostSlug}/*` — reverse proxy via tsnet using declared app auth contract
- [ ] UI: `ShareModal.svelte`, `AcceptInviteModal.svelte`, `RemoteAppTile.svelte`
- [ ] UI: Share option in app context menu, "Add shared app" in sidebar

Out of scope for MVP:

- Per-app sidecar on guest side (guest uses single tsnet outbound node)
- Federated OIDC / SSO between Bloud instances
- Real-time revocation notification to guest (guest discovers via 403)
- Guest-remove notification to host
- QR code generation
- Sharing multiple apps in one invite
- Full native-client credential sync for apps that cannot delegate auth
- Guest-visible audit log
- Re-sharing or delegation

---

## New Dependencies

| Package | Purpose |
|---|---|
| `tailscale.com/tsnet` | Guest host-agent outbound dialing through tailnet |
| `docker.io/tailscale/tailscale` | Sidecar container image (host side, not a Go dep) |

---

## Multi-Tailnet Architecture (Future)

MVP supports a single tailnet connection. The `tailnet_connections` table supports
multiple rows, but the orchestrator uses `GetActive()` which returns the first active.

Future: each app gets multiple sidecars, one per tailnet it's shared on. `shares`
gains a `tailnet_id` FK. `SidecarManager` creates sidecars per (app, tailnet) pair.
Node naming becomes `ts-{appName}-{tailnetId[:8]}`.

The `tailnet_connections` table schema:

```sql
CREATE TABLE IF NOT EXISTS tailnet_connections (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    type        TEXT NOT NULL,           -- 'tailscale' or 'headscale'
    auth_key    TEXT NOT NULL,
    control_url TEXT NOT NULL DEFAULT '', -- only for headscale
    status      TEXT NOT NULL DEFAULT 'active',
    created_at  TEXT DEFAULT (datetime('now'))
);
```

Connections are managed via the Settings page (`/settings`) or auto-migrated from the
`BLOUD_TS_AUTHKEY` environment variable on first startup.

---

## Open Questions

1. **Sidecar auth key management** — MVP uses a single reusable `TS_AUTHKEY` for all
   sidecars. Follow-on: generate ephemeral per-sidecar keys via Headscale/Tailscale API.

2. **Sidecar node naming** — `ts-{appName}` is the proposed format. Unique per app on a
   given host. If two Bloud hosts are on the same tailnet, node names will conflict.
   Follow-on: incorporate a host identifier (e.g. `ts-{appName}-{hostId[:8]}`).

---

## Implementation Plan

### Phase Dependencies

```
Phase 1 (sidecar spike) ─────────────────────────────────── GATE
     │
     ├── Phase 2 (orchestrator: sidecar lifecycle)
     │
     ├── Phase 3 (schema + store)
     │        │
     │        └── Phase 4 (invite generation)
     │                  │
     │                  └── Phase 5 (guest accept)
     │                            │
     │                            └── Phase 6 (proxy)
     │                                      │
     │                                 Phase 7 (revocation)
     │                                      │
     └──────────────────────────── Phase 8 (UI) ───┘
```

Phases 2 and 3 can run in parallel once Phase 1 passes. Phase 8 can be stubbed against
mock API responses while backend phases are in progress.

---

### Phase 1 — Sidecar spike (gate)

**Goal:** Prove the per-app Tailscale sidecar approach works inside the Lima VM before
building anything else. If the sidecar can't join the tailnet from inside a rootless
Podman network, the architecture needs to change. Everything else is blocked on this.

The sidecar uses Tailscale's userspace networking mode (`TS_USERSPACE=true`) so it does
not need `NET_ADMIN` capabilities, and Tailscale Serve to forward tailnet connections to
the app container.

**Manual spike — run by hand in Lima, no code committed until it passes:**
```bash
# 1. Start a sidecar alongside the running Navidrome container
podman run -d \
  --name ts-navidrome-spike \
  --network apps-net \
  --env TS_AUTHKEY=<authkey> \
  --env TS_HOSTNAME=navidrome-spike \
  --env TS_USERSPACE=true \
  --env TS_EXTRA_ARGS="--accept-routes" \
  docker.io/tailscale/tailscale:latest

# 2. Wait for it to join the tailnet, then get its address
podman exec ts-navidrome-spike tailscale ip --4
# → should print a 100.x.x.x address

# 3. Configure Serve to forward tailnet connections to Navidrome
podman exec ts-navidrome-spike \
  tailscale serve --bg http:4533 http://apps-navidrome:4533

# 4. From a second tailnet node (or tsnet in a Go test), reach Navidrome
curl http://<sidecar-tailnet-ip>:4533/ping
# → should return "."

# 5. Confirm node appears in Headscale
curl -sf -H "Authorization: Bearer <api-key>" <headscale-url>/api/v1/node \
  | jq '[.nodes[].name] | map(select(startswith("navidrome-spike")))'
# → ["navidrome-spike"]

# 6. Remove the spike sidecar
podman rm -f ts-navidrome-spike
```

**Gate criteria (all must pass before Phase 2/3):**
- Sidecar container starts in `apps-net` without `NET_ADMIN`
- `tailscale ip` returns an address within 30 seconds
- `curl` from a tailnet peer reaches Navidrome through the sidecar
- Removing the container causes subsequent requests to fail (tailnet access cut)

---

### Phase 2 — Orchestrator: sidecar lifecycle

**Files:** `internal/orchestrator/orchestrator_portable.go`,
`internal/sharing/sidecar.go`

The portable orchestrator already manages app container lifecycle. Extend it to start a
TS sidecar alongside every non-system app when `BLOUD_TS_AUTHKEY` is configured, and
stop it when the app stops.

```go
// internal/sharing/sidecar.go
type SidecarManager struct {
    podman  *podman.Client
    authKey string
    network string
}

// EnsureRunning starts the sidecar for appName if not already running.
// Blocks until the tailnet address is available or ctx is cancelled.
func (m *SidecarManager) EnsureRunning(ctx context.Context, appName string, appPort int) error

// GetAddr returns the current tailnet address of the sidecar, or error if not running.
func (m *SidecarManager) GetAddr(ctx context.Context, appName string) (string, error)

// Stop removes the sidecar container for appName.
func (m *SidecarManager) Stop(ctx context.Context, appName string) error

func sidecarContainerName(appName string) string {
    return "ts-" + appName
}
```

Sidecar container spec:
- Image: `docker.io/tailscale/tailscale:latest`
- Name: `ts-{appName}`
- Network: `apps-net`
- Env: `TS_AUTHKEY`, `TS_HOSTNAME=ts-{appName}`, `TS_USERSPACE=true`

After the container starts, `EnsureRunning` polls `podman exec ts-{appName} tailscale ip --4`
until an address is returned (30s timeout), then configures Serve:
`podman exec ts-{appName} tailscale serve --bg http:{port} http://apps-{appName}:{port}`.

If `BLOUD_TS_AUTHKEY` is not set, `SidecarManager` is nil and all sidecar calls are
no-ops. Apps work normally; sharing is unavailable.

#### Validation

**Fast (using fake Podman client):**
```
// internal/sharing/sidecar_test.go
// TestEnsureRunning_StartsAndConfiguresServe:
//   fake exec: tailscale ip returns "100.1.2.3", serve returns success
//   EnsureRunning → no error, serve configured
// TestEnsureRunning_Timeout:
//   fake exec always returns "" for tailscale ip
//   EnsureRunning with 100ms ctx → context.DeadlineExceeded
// TestEnsureRunning_AlreadyRunning:
//   container already exists → no duplicate create
// TestGetAddr_Success:
//   fake exec returns "100.1.2.3" → GetAddr returns "100.1.2.3"
// TestGetAddr_NotRunning:
//   fake exec returns error → GetAddr returns error
// TestStop_RemovesContainer:
//   Stop → fake client receives Remove("ts-navidrome")
// TestSidecarContainerName:
//   sidecarContainerName("navidrome") → "ts-navidrome"
// TestNilManagerIsNoop:
//   nil SidecarManager → EnsureRunning/Stop return nil, GetAddr returns error

go test ./internal/sharing/...
```

**Integration (Lima):**
```bash
# Install Navidrome, confirm sidecar starts automatically
./bloud install navidrome
sleep 10
limactl shell bloud-dev podman ps --filter name=ts-navidrome --format '{{.Names}}' \
  | grep -q ts-navidrome || { echo "FAIL: sidecar not running"; exit 1; }

ADDR=$(limactl shell bloud-dev podman exec ts-navidrome tailscale ip --4)
[ -n "$ADDR" ] || { echo "FAIL: no tailnet addr"; exit 1; }

curl -sf "http://$ADDR:4533/ping" | grep -q "." \
  || { echo "FAIL: not reachable via tailnet"; exit 1; }

# Uninstall Navidrome, confirm sidecar stops
./bloud uninstall navidrome
sleep 5
limactl shell bloud-dev podman ps --filter name=ts-navidrome --format '{{.Names}}' \
  | grep -v ts-navidrome || { echo "FAIL: sidecar still running after uninstall"; exit 1; }
echo "PASS"
```

---

### Phase 3 — Schema + store

**Files:** `internal/db/schema.sql`, `internal/testdb/testdb.go`,
`internal/store/shares.go`, `internal/store/remote_apps.go`

Add both tables (see Data Model above) and store implementations matching the existing
`apps.go` pattern.

```go
type ShareStore interface {
    Create(share Share) error
    GetByID(id string) (*Share, error)
    List() ([]*Share, error)
    Revoke(id string) error
}

type RemoteAppStore interface {
    Create(app RemoteApp) error
    GetByID(id string) (*RemoteApp, error)
    List() ([]*RemoteApp, error)
    SetRemoteUser(id, remoteUserID string) error
    SetStatus(id, status string) error
    Delete(id string) error
}
```

#### Validation

**Fast:**
```
// internal/db/schema_test.go
// TestSharesTableExists: INSERT a row, SELECT it back
// TestRemoteAppsTableExists: INSERT a row, SELECT it back
// TestShareStatusDefaultIsActive: INSERT share with no status → default='active'
// TestRemoteAppStatusDefault: default='pending_redemption'

// internal/store/shares_test.go
// TestShareStore_Create: GetByID returns row
// TestShareStore_Revoke: status → revoked, revoked_at set, row still readable
// TestShareStore_List: returns all non-deleted rows
// TestShareStore_RejectDuplicateID: second Create with same ID → error

// internal/store/remote_apps_test.go
// TestRemoteAppStore_Create: List returns it
// TestRemoteAppStore_SetRemoteUser: stores remote user id, GetByID returns it
// TestRemoteAppStore_SetStatus: transitions work
// TestRemoteAppStore_Delete: GetByID returns nil after Delete

go test ./internal/db/... ./internal/store/...
```

---

### Phase 4 — Invite generation

**Files:** `internal/sharing/token.go`, `internal/api/sharing.go`,
`internal/api/routes.go`

**Token format** — no external JWT dependency. A self-contained signed string:
```
base64url(json_payload) + "." + base64url(hmac_sha256(base64url(json_payload), secret))
```

**Payload:**
```json
{
  "shareId": "uuid",
  "appId": "navidrome",
  "appName": "Navidrome",
  "hostLabel": "Alice's Server",
  "ssoStrategy": "forward-auth",
  "bypassPaths": ["/rest/"],
  "provisioningStrategy": "trusted-header",
  "sidecarTailnetAddr": "100.1.2.3",
  "exp": 1234567890
}
```

Signed with `BLOUD_SECRET`. Single-use: the share row's `status` starts as `active`;
after the token is consumed on the guest side, the share is still `active` but the token's
`exp` has passed so it cannot be reused.

**Endpoints:**
```
POST /api/sharing/invites  { appId, guestLabel }
  → reads sidecar tailnet addr via SidecarManager.GetAddr
  → generates token (errors 503 if sidecar not running / TS not configured)
  → creates share row
  → returns { shareId, token }

GET  /api/sharing/shares   → list active shares
DELETE /api/sharing/shares/{id}  → revoke (Phase 7)
```

#### Validation

**Fast:**
```
// internal/sharing/token_test.go
// TestGenerateToken_RoundTrip: generate → parse → all claims present and correct
// TestGenerateToken_Expiry: past exp → error
// TestGenerateToken_TamperedSignature: flipped byte in sig → error
// TestGenerateToken_TamperedPayload: modified payload, original sig → error
// TestGenerateToken_MissingClaims: missing shareId → error
// TestGenerateToken_ForwardAuth:
//   token with ssoStrategy=forward-auth, bypassPaths=["/rest/"]
//   → parse → bypassPaths == ["/rest/"]

// internal/api/sharing_test.go
// TestHandleCreateInvite_Success:
//   POST /api/sharing/invites { appId: "navidrome", guestLabel: "bob" }
//   mock SidecarManager.GetAddr returns "100.1.2.3"
//   → 200, { shareId: "...", token: "eyJ..." }
//   → share row in DB with status=active
//   → token payload contains sidecarTailnetAddr="100.1.2.3"
// TestHandleCreateInvite_UnknownApp: → 404
// TestHandleCreateInvite_SidecarNotRunning: mock GetAddr returns error → 503
// TestHandleListShares_Empty: → []
// TestHandleListShares_WithData: seeded share → returned in list

go test ./internal/sharing/... ./internal/api/...
```

---

### Phase 5 — Guest accept and identity provisioning

**Files:** `internal/api/remote_apps.go`, `internal/sharing/provisioning.go`

The token is redeemed against the host Bloud instance so the host can create or bind a
Bloud user, mark the token consumed, and provision downstream app identity.

**Endpoints:**
```
POST /api/remote-apps/accept  { token, user_claims_or_session }
  → validate token (signature + expiry + unconsumed share)
  → create or bind host Bloud user
  → provision downstream app identity according to manifest capability
  → insert remote_app row (status=active, remote_user_id set)
  → return { shareId, appId, appName, bypassPaths }

GET  /api/remote-apps          → list remote apps
DELETE /api/remote-apps/{id}   → remove locally
```

Provisioning is adapter-specific:

- Trusted header apps get a deterministic app username and any required app-local user.
- Native OIDC/SAML apps get Authentik application/group membership.
- LDAP apps get required Authentik group membership.
- Degraded apps record that manual app-local login is still required.

#### Validation

**Fast:**
```
// internal/api/remote_apps_test.go
// TestHandleAcceptInvite_ForwardAuth:
//   POST /api/remote-apps/accept { token (ssoStrategy=forward-auth), user session }
//   → remote_app row created with status=active, bypassPaths=["/rest/"]
//   → remote_user_id stored
//   → downstream provisioner called with mapped app username
//   → response has { shareId, appId, appName, bypassPaths: ["/rest/"] }
// TestHandleAcceptInvite_ExpiredToken: → 400
// TestHandleAcceptInvite_BadSignature: → 400
// TestHandleAcceptInvite_DuplicateShareId: same shareId twice → 409
// TestHandleListRemoteApps_Empty: → []
// TestHandleListRemoteApps_WithData: seeded remote_app → returned
// TestHandleDeleteRemoteApp_Success: row deleted

go test ./internal/sharing/... ./internal/api/...
```

---

### Phase 6 — Proxy

**Files:** `internal/sharing/tsnode.go`, `internal/api/proxy.go`

**Guest tsnet node:**
```go
// internal/sharing/tsnode.go
type Node struct{ s *tsnet.Server }

func NewNode(authKey string) (*Node, error)  // no-op if authKey == ""
func (n *Node) Dial(ctx context.Context, network, addr string) (net.Conn, error)
```

Started in `cmd/host-agent/main.go` when `BLOUD_TS_AUTHKEY` is set. Used only for
outbound dialing — no listening ports.

**Proxy route:** `/embed/{appId}@{hostSlug}/*`

1. Parse `appId` and `hostSlug` from the path
2. Look up `RemoteApp` by `appId` + `hostSlug` (derived from `host_label`)
3. Return 404 if not found, 403 if revoked, 503 if tsnet unavailable
4. Reverse proxy to `sidecarTailnetAddr` via tsnet transport
5. Apply the app's declared proxy auth contract:
   - browser shared-login routes preserve the Bloud/Authentik session and allow the host
     proxy to inject trusted identity headers
   - native-client bypass routes pass through only declared paths and use app-specific
     native-client provisioning when required

For forward-auth apps, the proxy only accepts requests to declared `bypassPaths`. Requests
outside those paths return 404 unless the manifest explicitly supports browser sharing
through native SSO or trusted header auth.

```go
transport := &http.Transport{
    DialContext: tsNode.Dial,
}
proxy := httputil.NewSingleHostReverseProxy(target)
proxy.Transport = transport
```

#### Validation

**Fast:**
```
// internal/sharing/tsnode_test.go
// TestNodeNilWhenNoAuthKey: NewNode("") → nil, no error
// TestNodeDialErrorWhenNil: Dial on nil node → sentinel error

// internal/api/proxy_test.go
// TestProxy_ForwardAuth_AllowsDeclaredBypassPath:
//   httptest.Server records request path
//   remote_app: sidecarTailnetAddr=that server, bypassPaths=["/rest/"]
//   GET /embed/navidrome@alice-server/rest/getAlbumList2
//   → upstream receives /rest/getAlbumList2
// TestProxy_ForwardAuth_BlocksNonBypassPath:
//   GET /embed/navidrome@alice-server/web/ → 404
// TestProxy_UnknownApp: no remote_app row → 404
// TestProxy_PathPassthrough:
//   GET /embed/navidrome@alice-server/rest/ping
//   → upstream receives GET /rest/ping (prefix stripped)
// TestProxy_CustomDialer:
//   fake tsnet Dial returns net.Conn to test server
//   → proxy uses it, confirming WireGuard path will be taken in production

go test ./internal/sharing/... ./internal/api/...
```

**Integration (Lima):**
```bash
# After Phase 5 integration: share is accepted, remote_app row exists

curl -sf http://localhost:8080/embed/navidrome@alice-server/rest/ping \
  | grep -q "ok" || { echo "FAIL: proxy ping"; exit 1; }

# Revoke the share → expect 403 from Bloud proxy
curl -sf -X DELETE http://localhost:3000/api/sharing/shares/$SHARE_ID
curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/navidrome@alice-server/rest/ping \
  | grep -q 403 || { echo "FAIL: revoked share should 403"; exit 1; }

echo "PASS"
```

---

### Phase 7 — Revocation

**Files:** `internal/api/sharing.go` (complete the revocation handler)

`DELETE /api/sharing/shares/{id}`:
1. Look up share — 404 if not found
2. Mark share revoked in DB
3. Remove or disable downstream authorization where the app adapter supports it
4. Return `{ status: "revoked", cleanupStatus: string }`

The sidecar is not touched — it keeps running with the app. The guest's proxy returns
403 on the next request because the share row is revoked.

#### Validation

**Fast:**
```
// internal/api/sharing_test.go (extend)
// TestHandleRevokeShare_ForwardAuth:
//   DELETE /api/sharing/shares/{id} (sso_strategy=forward-auth)
//   → share row status=revoked
//   → downstream deprovisioner called when supported
//   → SidecarManager NOT called (sidecar unaffected)
// TestHandleRevokeShare_UnknownShare: → 404
// TestProxyAfterRevocation:
//   revoke share → GET /embed/navidrome@alice-server/rest/ping → 403

go test ./internal/api/...
```

**Integration (Lima):**
```bash
curl -sf -X DELETE http://localhost:3000/api/sharing/shares/$SHARE_ID \
  | jq -e '.status == "revoked"' || { echo "FAIL: revoke failed"; exit 1; }

# Sidecar must still be running (tied to app, not share)
limactl shell bloud-dev podman ps --filter name=ts-navidrome --format '{{.Names}}' \
  | grep -q ts-navidrome || { echo "FAIL: sidecar should still be running"; exit 1; }

# Proxy must 403
curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/navidrome@alice-server/rest/ping \
  | grep -q 403 || { echo "FAIL: proxy should 403 after revocation"; exit 1; }

echo "PASS"
```

---

### Phase 8 — UI

**New files:**
- `web/src/lib/components/ShareModal.svelte` — owner shares an app; shows the invite
  token string (copy-to-clipboard), guest label input, list of active shares with revoke
  buttons and cleanup status
- `web/src/lib/components/AcceptInviteModal.svelte` — paste token, create or bind the
  host Bloud user, submit
- `web/src/lib/components/RemoteAppTile.svelte` — extends `AppTile.svelte`; shows
  "Hosted by X" badge, offline/revoked states

**Modified files:**
- `AppContextMenu.svelte` — add "Share" option (installed, non-system apps only)
- `+page.svelte` — fetch `/api/remote-apps`, render in dashboard grid
- `Sidebar.svelte` — "Add shared app" entry point

#### Validation

**Playwright (`web/tests/sharing.test.ts`):**
```
// TestShareFlow:
//   Right-click Navidrome tile → Share → guest label input, token string appears
//   Copy token button → clipboard contains the token string
//   Share list shows "bob — active"

// TestAcceptFlow:
//   (Invite token seeded via API before test)
//   Sidebar → Add shared app → paste token
//   Account redemption form appears
//   Create or bind host Bloud user → Submit
//   Dashboard shows Navidrome with "Hosted by Alice's Server" badge

// TestRevokeFlow_ForwardAuth:
//   (Share seeded via API before test)
//   ShareModal → share list shows "bob — active"
//   Click Revoke → confirm dialog summarizes downstream cleanup
//   Confirm → row gone, toast "Access revoked."

// TestOfflineBadge:
//   (remote_app seeded with status=offline via API before test)
//   Dashboard shows app with offline indicator
//   Clicking it shows "App unavailable", not a broken page

npx playwright test tests/sharing.test.ts --reporter=line
```

---

## Running Tests

### After every change (fast tier)

```bash
cd services/host-agent
go test ./internal/sharing/... ./internal/store/... ./internal/api/... ./internal/db/...
```

### Integration tier (Lima VM required)

```bash
bash scripts/test-sharing-sidecar.sh   # Phase 2
bash scripts/test-sharing-invite.sh    # Phase 4
bash scripts/test-sharing-proxy.sh     # Phase 6
bash scripts/test-sharing-revoke.sh    # Phase 7
```

### UI tier

```bash
cd services/host-agent/web
npx playwright test tests/sharing.test.ts --reporter=line
```
