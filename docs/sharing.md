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

Each shared app gets a dedicated Tailscale sidecar container in the same Podman network
as the app. This sidecar:

- Joins the tailnet as its own node (e.g. `navidrome-a1b2c3d4`)
- Uses Tailscale Serve to proxy incoming tailnet connections to the app's container port
- Is the only entry point into the app over the tailnet

Sharing an app = starting the sidecar. Revoking = stopping and removing the sidecar
container. No other apps on the host are reachable through this path.

```
┌─────────────────────────────────────────────────┐
│ Alice's Bloud Host                               │
│                                                  │
│  apps-net:                                       │
│  ┌────────────┐     ┌──────────────────────┐     │
│  │ navidrome  │◄────│ ts-navidrome-a1b2c3d4│─────┼── tailnet
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

## User Identity and Credentials

For MVP, the owner creates the guest's app account manually and shares the credentials
as part of the invite process. The guest enters those credentials when accepting the invite.
Bloud stores them encrypted on the guest side and injects them on every proxied request.

- Credentials are app-local — they are not the guest's Bloud account credentials.
- If the owner changes the guest's password in the app, the guest must update their stored
  credential via **Shared Apps → [App] → Update Stored Password**.
- Bloud does not automatically sync credential changes. This is an intentional MVP
  simplification.

This model works for all MVP-scope apps:
- **Forward-auth apps (Navidrome):** owner creates a Navidrome user, shares username +
  password. The Subsonic client path (`/rest/`) bypasses forward-auth and accepts these
  credentials directly.
- **LDAP apps (Jellyfin):** owner creates a local Jellyfin user (not via Authentik/LDAP),
  shares credentials. Native Jellyfin clients use these to authenticate.

### Future: Federated SSO

After MVP, the intent is to replace owner-provisioned credentials with OIDC federation
between Bloud instances. The guest's Bloud acts as an OIDC identity provider; the host's
Authentik accepts it as a trusted external IdP. This means the guest authenticates once
with their own credentials and the identity is accepted everywhere they've been invited.

This is explicitly out of scope for the first release.

---

## Forward-Auth Apps and Native Client Sharing

Some apps use Authentik forward-auth for their web UI but also serve native protocol
clients (mobile apps, desktop clients) through a separate API path that has its own
credential scheme. Navidrome is the canonical example: the web UI goes through forward-auth,
but Subsonic clients authenticate against Navidrome's `/rest/` endpoint directly using
the Subsonic credential scheme.

For these apps, Bloud emits a bypass route in Traefik for the declared API paths (e.g.
`/rest/`) that skips forward-auth middleware. This is an accepted trade-off — the Subsonic
protocol has no mechanism for delegating authentication to a third party.

### What Gets Shared

For forward-auth apps, sharing targets the **native client path**, not the web UI:

- The guest's Bloud proxy exposes the bypass path (e.g. `/rest/`) via the tailnet
- The guest configures their native client (Subsonic app, RSS reader, etc.) to point at
  the guest's local Bloud proxy address
- The proxy forwards requests over the tailnet to the host's bypass route, injecting the
  guest's stored credential

The web UI path (which requires an Authentik session) is **not** shared in this flow.
Web UI sharing for forward-auth apps requires federated SSO between Bloud instances and
is explicitly deferred to the post-MVP roadmap.

### Revocation for Forward-Auth Apps

Revocation removes the sidecar container (cutting tailnet access) and marks the share
revoked. Because Bloud never provisioned an Authentik user for the guest, there is nothing
else to clean up automatically. The revocation UI reminds the owner to delete the guest's
in-app account manually: "Don't forget to remove the user from Navidrome."

---

## Invite Flow

```
1. OWNER CREATES APP ACCOUNT FOR GUEST
   Owner creates a user in the app (e.g. Navidrome's user management UI)
   and sets a password. This is a manual step — Bloud cannot provision
   app-local accounts automatically for forward-auth/LDAP apps.

2. OWNER CREATES INVITE
   Owner clicks Share in Bloud UI.
   → Bloud starts the Tailscale sidecar container for the app
   → Sidecar joins the tailnet and Bloud retrieves its tailnet address
   → Bloud generates a signed invite token containing:
       - shareId
       - appId, appName, hostLabel
       - ssoStrategy, bypassPaths
       - sidecarTailnetAddr (the address to proxy through)
       - expiry (1 hour)
   → Token displayed as a string for copy/paste
   Owner shares the token with the guest out of band (message, etc.)

3. GUEST ACCEPTS
   Guest pastes the token into their Bloud UI.
   Bloud validates the signature and expiry locally — no network call needed.
   Guest is prompted: "Enter the credentials Alice created for you."
   Guest enters username + password, clicks Accept.
   Bloud stores the remote app entry and encrypted credential locally.
   No call is made to the host at this point.

4. GUEST BLOUD UI
   Dashboard shows the app with a "Hosted by Alice's Server" badge.
   Guest configures their native client to point at the local proxy address.
```

---

## Proxy Behavior

### All Clients (Default Path)

The guest's Bloud exposes the remote app at a local path:

```
localhost:8080/embed/navidrome@alice-server/
```

Requests to this path are reverse-proxied over the tailnet to the host's sidecar. The
proxy injects the guest's stored credential so the host app authenticates the request.
This works for any client, including dumb clients that cannot join a tailnet themselves.

For forward-auth apps, the proxy routes only to the declared bypass paths (e.g. `/rest/`).
Requests to other paths return 404 — there is no web UI to proxy.

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
- Bloud stops and removes the sidecar container (tailnet access immediately cut)
- Share record marked revoked
- For forward-auth apps: UI reminder to delete the guest's in-app account manually
- Guest discovers revocation on next proxy request (returns 403)

**Guest-initiated:**
- Guest removes the shared app from their Bloud UI
- RemoteApp row deleted from guest DB
- Sidecar continues running on host until owner revokes independently

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
| Credentials are app-local, not tied to guest's Bloud account | Owner provisions a separate credential; guest stores it locally |
| Invites are single-use and time-limited | HMAC-signed token, 1-hour TTL, server-side consumed flag |
| No transitive sharing | Guest cannot produce invite tokens for apps they don't own |
| Revocation is immediate | Sidecar container removed — tailnet node disappears |

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

Note: status starts as `active` (not `pending`) because the sidecar is running and the
token is generated in a single synchronous operation.

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
    encrypted_cred       BLOB,               -- AES-256-GCM; null until accepted
    status               TEXT NOT NULL DEFAULT 'pending_credential',
    created_at           TEXT DEFAULT (datetime('now'))
);
```

---

## MVP Scope

In scope:

- [ ] Per-app Tailscale sidecar container started when a share is created
- [ ] Sidecar tailnet address retrieval (`podman exec tailscale ip`)
- [ ] DB schema: `shares` and `remote_apps` tables
- [ ] Signed invite token generation (stdlib HMAC-SHA256, no external JWT dep)
- [ ] `POST /api/sharing/invites` — start sidecar, wait for tailnet addr, return token
- [ ] `GET /api/sharing/shares` — list active shares (owner view)
- [ ] `DELETE /api/sharing/shares/{id}` — revoke: stop + remove sidecar, mark revoked
- [ ] `POST /api/remote-apps/accept` — validate token, store credential locally
- [ ] `GET /api/remote-apps` — list remote apps (guest dashboard)
- [ ] `DELETE /api/remote-apps/{id}` — guest removes (local only)
- [ ] `tsnet` node in guest host-agent for outbound dialing
- [ ] `/embed/{appId}@{hostSlug}/*` — reverse proxy via tsnet with credential injection
- [ ] UI: `ShareModal.svelte`, `AcceptInviteModal.svelte`, `RemoteAppTile.svelte`
- [ ] UI: Share option in app context menu, "Add shared app" in sidebar

Out of scope for MVP:

- Per-app sidecar on guest side (guest uses single tsnet outbound node)
- Federated OIDC / SSO between Bloud instances
- Authentik user auto-provisioning for sharing (deferred with native-OIDC sharing)
- Real-time revocation notification to guest (guest discovers via 403)
- Guest-remove notification to host
- QR code generation
- Sharing multiple apps in one invite
- Credential sync / rotation push to host
- Guest-visible audit log
- Re-sharing or delegation

---

## New Dependencies

| Package | Purpose |
|---|---|
| `tailscale.com/tsnet` | Guest host-agent outbound dialing through tailnet |
| `docker.io/tailscale/tailscale` | Sidecar container image (host side, not a Go dep) |

---

## Open Questions

1. **Credential storage encryption** — what key protects `encrypted_cred` on the guest
   side? Deferred until the broader secrets management design is settled. For MVP, derive
   from `BLOUD_SECRET` with a fixed salt.

2. **Sidecar auth key management** — MVP uses a single reusable `TS_AUTHKEY` for all
   sidecars. Follow-on: generate ephemeral per-sidecar keys via Headscale/Tailscale API.

3. **Sidecar node naming** — `{appName}-{shareId[:8]}` is the proposed format. Needs to
   be unique per host across all shares.

---

## Implementation Plan

### Phase Dependencies

```
Phase 1 (sidecar spike) ─────────────────────────────────── GATE
     │
     ├── Phase 2 (schema + store)
     │        │
     │        ├── Phase 3 (sidecar lifecycle)
     │        │        │
     │        │        └── Phase 4 (invite generation)
     │        │                  │
     │        │                  └── Phase 5 (guest accept)
     │        │                            │
     │        │                            └── Phase 6 (proxy)
     │        │                                      │
     │        │                                 Phase 7 (revocation)
     │        │                                      │
     └────────┴──────────────────── Phase 8 (UI) ───┘
```

Phase 8 can be stubbed against mock API responses while backend phases are in progress.

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

**Gate criteria (all must pass before Phase 2):**
- Sidecar container starts in `apps-net` without `NET_ADMIN`
- `tailscale ip` returns an address within 30 seconds
- `curl` from a tailnet peer reaches Navidrome through the sidecar
- Removing the container causes subsequent requests to fail (tailnet access cut)

---

### Phase 2 — Schema + store

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
    SetCredential(id string, encryptedCred []byte) error
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
// TestRemoteAppStatusDefault: default='pending_credential'

// internal/store/shares_test.go
// TestShareStore_Create: GetByID returns row
// TestShareStore_Revoke: status → revoked, revoked_at set, row still readable
// TestShareStore_List: returns all non-deleted rows
// TestShareStore_RejectDuplicateID: second Create with same ID → error

// internal/store/remote_apps_test.go
// TestRemoteAppStore_Create: List returns it
// TestRemoteAppStore_SetCredential: stores bytes, GetByID returns them
// TestRemoteAppStore_SetStatus: transitions work
// TestRemoteAppStore_Delete: GetByID returns nil after Delete

go test ./internal/db/... ./internal/store/...
```

---

### Phase 3 — Sidecar lifecycle

**Files:** `internal/sharing/sidecar.go`

Manages Tailscale sidecar containers. Uses the existing Podman client.

```go
type SidecarManager struct {
    podman    *podman.Client
    authKey   string
    network   string
}

// Start creates and starts the sidecar for a share. Blocks until the tailnet
// address is available or ctx is cancelled.
func (m *SidecarManager) Start(ctx context.Context, shareID, appName string, appPort int) (tailnetAddr string, err error)

// Stop removes the sidecar container for a share.
func (m *SidecarManager) Stop(ctx context.Context, shareID string) error

// containerName returns the deterministic container name for a share's sidecar.
func containerName(shareID string, appName string) string {
    return fmt.Sprintf("ts-%s-%s", appName, shareID[:8])
}
```

`Start` creates a container with:
- Image: `docker.io/tailscale/tailscale:latest`
- Name: `ts-{appName}-{shareId[:8]}`
- Network: `apps-net`
- Env: `TS_AUTHKEY`, `TS_HOSTNAME={name}`, `TS_USERSPACE=true`

After the container starts, `Start` polls `podman exec {name} tailscale ip --4` with
a 30-second timeout. Once an address is returned, it runs
`podman exec {name} tailscale serve --bg http:{appPort} http://{appContainerName}:{appPort}`
to configure Serve, then returns the address.

#### Validation

**Fast (using fake Podman client):**
```
// internal/sharing/sidecar_test.go
// TestSidecarManager_Start_ReturnsAddr:
//   fake podman exec: first call returns "", second returns "100.1.2.3"
//   Start → "100.1.2.3", no error
// TestSidecarManager_Start_Timeout:
//   fake podman exec always returns ""
//   Start with 100ms ctx → context.DeadlineExceeded
// TestSidecarManager_Stop_RemovesContainer:
//   Stop → fake client receives Remove("ts-navidrome-a1b2c3d4")
// TestContainerName_Format:
//   containerName("a1b2c3d4e5f6...", "navidrome") → "ts-navidrome-a1b2c3d4"

go test ./internal/sharing/...
```

**Integration (Lima):**
```bash
# After Phase 1 gate passes, this runs against real Podman + real tailnet
SHARE_ID="testshare123"
ADDR=$(go run ./cmd/sharing-spike start $SHARE_ID navidrome 4533)
[ -n "$ADDR" ] || { echo "FAIL: no addr"; exit 1; }
curl -sf "http://$ADDR:4533/ping" | grep -q "." || { echo "FAIL: ping"; exit 1; }
go run ./cmd/sharing-spike stop $SHARE_ID
curl "http://$ADDR:4533/ping" 2>&1 | grep -q "refused\|timeout" \
  || { echo "FAIL: should be unreachable after stop"; exit 1; }
echo "PASS"
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
  → starts sidecar (Phase 3), generates token
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
//   mock SidecarManager.Start returns "100.1.2.3"
//   → 200, { shareId: "...", token: "eyJ..." }
//   → share row in DB with status=active
//   → token payload contains sidecarTailnetAddr="100.1.2.3"
// TestHandleCreateInvite_UnknownApp: → 404
// TestHandleCreateInvite_SidecarStartFails: mock Start returns error → 502
// TestHandleListShares_Empty: → []
// TestHandleListShares_WithData: seeded share → returned in list

go test ./internal/sharing/... ./internal/api/...
```

---

### Phase 5 — Guest accept

**Files:** `internal/sharing/crypto.go`, `internal/api/remote_apps.go`

No network call to the host. The guest validates the token locally, enters credentials,
and Bloud stores everything it needs to proxy.

**Endpoints:**
```
POST /api/remote-apps/accept  { token, username, password }
  → validate token (signature + expiry)
  → encrypt credential
  → insert remote_app row (status=active)
  → return { shareId, appId, appName, bypassPaths }

GET  /api/remote-apps          → list remote apps
DELETE /api/remote-apps/{id}   → remove locally
```

Credential encryption: AES-256-GCM. Key derived from `BLOUD_SECRET` with a fixed
`sharing-cred` salt using HKDF-SHA256. IV is random and prepended to the ciphertext.

#### Validation

**Fast:**
```
// internal/sharing/crypto_test.go
// TestEncryptDecryptRoundtrip: encrypt → decrypt → bytes match
// TestDecryptWrongKey: different key → error
// TestDecryptTamperedCiphertext: flip a byte → error

// internal/api/remote_apps_test.go
// TestHandleAcceptInvite_ForwardAuth:
//   POST /api/remote-apps/accept { token (ssoStrategy=forward-auth), username, password }
//   → remote_app row created with status=active, bypassPaths=["/rest/"]
//   → encrypted_cred stored (decrypt → original password)
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
4. Decrypt stored credential
5. Reverse proxy to `sidecarTailnetAddr` via tsnet transport, injecting
   `Authorization: Basic base64(username:password)`

For forward-auth apps, the proxy only accepts requests to declared `bypassPaths`. Requests
outside those paths return 404.

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
// TestProxy_ForwardAuth_InjectsBasicAuth:
//   httptest.Server records Authorization header
//   remote_app: sidecarTailnetAddr=that server, bypassPaths=["/rest/"], cred=bob:hunter2
//   GET /embed/navidrome@alice-server/rest/getAlbumList2
//   → upstream receives Authorization: Basic <base64(bob:hunter2)>
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

# Corrupt credential → expect 401 from Navidrome (proves injection working)
limactl shell bloud-dev sqlite3 ~/bloud-data/bloud.db \
  "UPDATE remote_apps SET encrypted_cred=X'deadbeef' WHERE app_id='navidrome'"
curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/navidrome@alice-server/rest/ping \
  | grep -q 401 || { echo "FAIL: bad cred should 401"; exit 1; }

echo "PASS"
```

---

### Phase 7 — Revocation

**Files:** `internal/api/sharing.go` (complete the revocation handler)

`DELETE /api/sharing/shares/{id}`:
1. Look up share — 404 if not found
2. Stop and remove the sidecar container (`SidecarManager.Stop`)
3. Mark share revoked in DB
4. Return `{ status: "revoked", requiresManualCleanup: bool, cleanupHint: string }`
   - `requiresManualCleanup: true` and a hint for forward-auth apps
   - `requiresManualCleanup: false` for native-OIDC apps (post-MVP)

Sidecar stop is best-effort: if the container is already gone, log and continue. The
share is marked revoked regardless.

#### Validation

**Fast:**
```
// internal/api/sharing_test.go (extend)
// TestHandleRevokeShare_ForwardAuth:
//   DELETE /api/sharing/shares/{id} (sso_strategy=forward-auth)
//   → mock SidecarManager.Stop called with correct shareID
//   → share row status=revoked
//   → response: requiresManualCleanup=true, cleanupHint contains "Navidrome"
// TestHandleRevokeShare_SidecarAlreadyGone:
//   SidecarManager.Stop returns "not found" error
//   → revocation still completes, no error returned
// TestHandleRevokeShare_UnknownShare: → 404
// TestProxyAfterRevocation:
//   revoke share → GET /embed/navidrome@alice-server/rest/ping → 403

go test ./internal/api/...
```

**Integration (Lima):**
```bash
curl -sf -X DELETE http://localhost:3000/api/sharing/shares/$SHARE_ID \
  | jq -e '.status == "revoked"' || { echo "FAIL: revoke failed"; exit 1; }

# Sidecar container must be gone
limactl shell bloud-dev podman ps -a --filter name=ts-navidrome \
  | grep -v "ts-navidrome" || { echo "FAIL: sidecar still running"; exit 1; }

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
  buttons and manual cleanup reminder for forward-auth apps
- `web/src/lib/components/AcceptInviteModal.svelte` — paste token, enter credentials
  (with hint text explaining these are provided by the owner), submit
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
//   Credential form appears with hint "Enter the credentials Alice created for you"
//   Fill username + password → Submit
//   Dashboard shows Navidrome with "Hosted by Alice's Server" badge

// TestRevokeFlow_ForwardAuth:
//   (Share seeded via API before test)
//   ShareModal → share list shows "bob — active"
//   Click Revoke → confirm dialog shows manual cleanup reminder
//   Confirm → row gone, toast "Access revoked. Don't forget to remove bob from Navidrome."

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
bash scripts/test-sharing-sidecar.sh   # Phase 3
bash scripts/test-sharing-invite.sh    # Phase 4
bash scripts/test-sharing-proxy.sh     # Phase 6
bash scripts/test-sharing-revoke.sh    # Phase 7
```

### UI tier

```bash
cd services/host-agent/web
npx playwright test tests/sharing.test.ts --reporter=line
```
