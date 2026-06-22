# Bloud Sharing

**Status:** Proposed  
**Last updated:** 2026-06-20

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
  removing their account from the app and invalidating their tailnet access.

---

## Network Architecture

### Target: Tailscale Sidecar Per App

Each app gets a dedicated Tailscale sidecar container in the same Podman pod. This sidecar:

- Joins the tailnet as its own node (e.g. `jellyfin.alice-server`)
- Advertises only that app's internal port — not the host network
- Is the only entry point into the app over the tailnet

Sharing an app = granting a guest's node access to that app's tailnet node. Revoking =
removing that access. No other apps on the host are reachable through this path.

```
┌─────────────────────────────────────────────────┐
│ Alice's Bloud Host                               │
│                                                  │
│  ┌──────────────────────┐                        │
│  │ Jellyfin Pod          │                        │
│  │  ┌────────────┐       │                        │
│  │  │ jellyfin   │:8096  │                        │
│  │  └────────────┘       │                        │
│  │  ┌────────────┐       │                        │
│  │  │ tailscale  │───────┼──── tailnet ──────┐   │
│  │  │  sidecar   │       │                   │   │
│  │  └────────────┘       │                   │   │
│  └──────────────────────┘                    │   │
└──────────────────────────────────────────────┼───┘
                                               │
                                         WireGuard (P2P)
                                               │
┌──────────────────────────────────────────────┼───┐
│ Bob's Bloud Host                             │   │
│                                              │   │
│  ┌─────────────────────────────────────┐     │   │
│  │ Bloud Proxy                          │     │   │
│  │  /embed/jellyfin@alice/ ────────────┼─────┘   │
│  └─────────────────────────────────────┘         │
│          ▲                                        │
│          │ localhost:8080                         │
└──────────┼────────────────────────────────────────┘
           │
      Bob's TV / phone / browser
```

### MVP Simplification: tsnet in host-agent

The per-app sidecar is the target architecture. For the MVP, the host-agent process
itself joins the tailnet using the [`tailscale.com/tsnet`](https://pkg.go.dev/tailscale.com/tsnet)
Go library. This gives one tailnet node per Bloud host rather than one per app.
App-level isolation in the MVP is enforced by the guest's credential, not the tailnet
topology. Per-app sidecars are a follow-on slice.

### Headscale / Tailscale Configuration

Bloud does not prescribe which control plane is used. The owner configures one of:

- **Tailscale** (commercial, easiest) — authenticate with a Tailscale auth key
- **Self-hosted Headscale** — run your own Headscale instance on a VPS
- **Community Headscale** — a community member runs a Headscale instance for a group

In all cases, Bloud only needs a `TS_AUTHKEY`. The control plane choice is an operator
concern. Headscale's control plane sees node registrations and coordinates key exchange,
but all data traffic is direct peer-to-peer WireGuard — the control plane operator cannot
read app traffic.

---

## User Identity and Credentials

### MVP: Guest-Chosen Credentials, No Sync

The guest chooses their own username and password for the remote app during the invite
acceptance flow. Bloud stores that credential for use by the local proxy. After that,
credential management is the guest's responsibility.

- The guest's chosen credentials can differ from their local Bloud credentials.
- If the guest changes their password directly on the host (e.g. via the host's app UI),
  they must also update the stored credential in their Bloud UI under
  **Shared Apps → [App] → Update Stored Password** so the proxy keeps working.
- Bloud does not automatically sync credential changes. This is an intentional MVP
  simplification.

### Future: Federated SSO

After MVP, the intent is to replace guest-chosen credentials with OIDC federation between
Bloud instances. The guest's Bloud acts as an OIDC identity provider; the host's Authentik
accepts it as a trusted external IdP. This means the guest authenticates once with their
own credentials and the identity is accepted everywhere they've been invited.

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

### Invite Flow for Forward-Auth Apps

The invite flow is simplified compared to native-OIDC apps:

1. **Owner creates the guest's app account manually.** Because Bloud cannot auto-provision
   an Authentik user and have it work for Subsonic clients, the owner creates a user
   directly inside the app (e.g. Navidrome's user management UI) and sets a password.

2. **Owner creates an invite as normal.** The invite blob encodes the app, the host's
   tailnet address, and which paths are being shared.

3. **Guest accepts and enters the credentials the owner provided.** Instead of choosing
   their own credentials, the guest enters the username and password the owner created
   for them. These are stored on the guest side and injected by the proxy.

4. **Guest configures their native client.** The guest points their app (e.g. a Subsonic
   client) at their local Bloud proxy. From the client's perspective it's a normal server.

The metadata field `sso.bypassPaths` on the app definition tells Bloud which paths to
share and which credential scheme is in use, so this flow is triggered automatically
for any app that declares bypass paths.

### Revocation

Revocation requires the owner to delete the guest's account inside the app manually,
in addition to Bloud revoking tailnet access. Bloud surfaces this as a reminder in the
revocation confirmation UI: "Don't forget to remove the user from Navidrome."

This is a deliberate limitation of apps that don't support centralized identity — there
is no API for Bloud to call to delete a Navidrome user on behalf of the owner in the
sharing context.

---

## Invite Flow

```
1. OWNER CREATES INVITE
   bloud share jellyfin
   → Host's Bloud pre-provisions a pending share record
   → Generates a signed, single-use invite blob (JWT, 1-hour TTL) containing:
       - shareId
       - Host's Bloud tailnet address (for post-join communication)
       - App metadata (name, icon, type)
       - Expiry
   → Displays as a QR code in the Bloud UI (and raw string for copy/paste)
   Owner shares the QR code or string with the guest out of band (message, etc.)
   Host does not need a public address — the blob is self-contained.

2. GUEST ACCEPTS
   Guest scans the QR code or pastes the string into their Bloud UI.
   Bloud validates the blob locally (signature, expiry) before doing anything.
   No network call to the host is needed at this stage.
   Both sides must already be on the same tailnet (same Headscale/Tailscale instance).

3. GUEST CALLS HOST OVER TAILNET
   Guest's Bloud calls host's Bloud at the tailnet address from the blob:
     POST /api/sharing/shares/{shareId}/accept { guestEmail, displayName, guestTailnetURL }
   Host creates the pending Authentik user and app account.
   Host records the guest's tailnet address for future revocation notifications.
   Guest stores a pending RemoteApp entry.

4. GUEST SETS CREDENTIALS
   Guest is prompted: "Choose your username and password for Jellyfin on Alice's Server."
   Guest's Bloud sends the chosen credential to the host via the tailnet:
     POST /api/sharing/shares/{shareId}/credential { username, password }
   Host sets the password in Authentik and the app account. Share status → active.
   Guest's Bloud encrypts and stores the credential. RemoteApp status → active.

5. GUEST BLOUD UI
   Dashboard shows Jellyfin with a "Hosted by Alice's Server" badge.
```

---

## Proxy Behavior

### All Clients (Default Path)

The guest's Bloud exposes the remote app at a local path:

```
localhost:8080/embed/jellyfin@alice-server/
```

Requests to this path are reverse-proxied over the tailnet to the host app. The proxy
injects the guest's credential so the host app authenticates the request. This works for
any client, including dumb clients that cannot join a tailnet themselves.

### Smart Clients (Direct Path)

For clients capable of connecting directly to the tailnet (e.g. Jellyfin mobile app,
browser), the guest can retrieve the tailnet address from the Bloud UI and configure the
client to connect directly. This avoids the proxy hop and reduces latency — relevant for
high-bitrate video.

### Offline / Host Unreachable

If the host is unreachable over the tailnet, the proxy returns a clear error. No content
is cached. The guest's Bloud UI marks the app as offline.

---

## Revocation

**Owner-initiated:**
- Host Bloud deletes the guest's Authentik user and app account
- Share record marked revoked
- Notification sent to guest's tailnet address (failure is non-fatal; guest discovers
  revocation on next proxy request — returns 403)

**Guest-initiated:**
- Guest removes the shared app from their Bloud UI
- Notifies host to clean up accounts
- RemoteApp row deleted from guest DB

**Node expiry:**
If the tailnet connection drops, the proxy fails and the UI shows the app as offline.
The owner can re-invite to restore access.

---

## Security Properties

| Property | Mechanism |
|---|---|
| Only invited users reach the app | Sharing is at the Tailscale node level — guest's node is granted access to specific app sidecar nodes, and denied all others |
| Traffic is encrypted in transit | WireGuard (Tailscale/Headscale) end-to-end |
| Control plane can't read traffic | P2P WireGuard; Headscale operator sees node list only |
| Credentials are guest-chosen, not tied to their main account | Guest sets a username/password for the remote app at invite time; their local Bloud account is unaffected |
| Invites are single-use and time-limited | Signed JWT, 1-hour TTL, server-side consumed flag |
| No transitive sharing | Guest cannot produce invite tokens for apps they don't own |
| Revocation is immediate | Account deletion + Tailscale node access revocation in one host-side operation |

---

## Data Model

### Host side — `shares` table

```sql
CREATE TABLE IF NOT EXISTS shares (
    id                   TEXT PRIMARY KEY,
    app_id               TEXT NOT NULL,
    guest_email          TEXT NOT NULL,
    guest_tailnet_addr   TEXT,
    tailscale_node_id    TEXT,
    status               TEXT NOT NULL DEFAULT 'pending',  -- pending | active | revoked
    created_at           TEXT DEFAULT (datetime('now')),
    revoked_at           TEXT
);
```

### Guest side — `remote_apps` table

```sql
CREATE TABLE IF NOT EXISTS remote_apps (
    id               TEXT PRIMARY KEY,
    host_label       TEXT NOT NULL,
    host_tailnet_url TEXT NOT NULL,
    app_id           TEXT NOT NULL,
    app_name         TEXT NOT NULL,
    tailnet_addr     TEXT NOT NULL,
    encrypted_cred   BLOB,                      -- AES-256-GCM; null until credential step
    status           TEXT NOT NULL DEFAULT 'pending_credential',
    created_at       TEXT DEFAULT (datetime('now'))
);
```

---

## MVP Scope

In scope:

- [ ] `tsnet` node started in host-agent when `BLOUD_TS_AUTHKEY` is configured
- [ ] DB schema: `shares` and `remote_apps` tables
- [ ] Authentik: `CreateUser`, `DeleteUser`, `SetUserPassword`
- [ ] `POST /api/sharing/invites` — generate invite blob + QR code
- [ ] `GET /api/sharing/shares` — list active shares (owner view)
- [ ] `DELETE /api/sharing/shares/{id}` — owner revokes
- [ ] `POST /api/sharing/shares/{shareId}/accept` — host-side accept (called by guest over tailnet)
- [ ] `POST /api/sharing/shares/{shareId}/credential` — host-side credential set
- [ ] `POST /api/remote-apps/accept` — guest accepts invite
- [ ] `POST /api/remote-apps/{shareId}/credential` — guest sets credentials
- [ ] `GET /api/remote-apps` — list remote apps (guest dashboard)
- [ ] `DELETE /api/remote-apps/{shareId}` — guest removes
- [ ] `/embed/{appId}@{hostSlug}/*` — reverse proxy over tailnet with credential injection
- [ ] UI: `ShareModal.svelte`, `AcceptInviteModal.svelte`, `RemoteAppTile.svelte`
- [ ] UI: Share option in app context menu, "Add shared app" in sidebar

Out of scope for MVP:

- Per-app Tailscale sidecar containers (follow-on slice)
- Federated OIDC / SSO between Bloud instances
- Sharing multiple apps in one invite
- Credential sync / rotation push to host
- Sharing settings page (auth key configured via env var for now)
- Guest-visible audit log
- Bandwidth / usage metering
- Re-sharing or delegation

---

## New Dependencies

| Package | Purpose |
|---|---|
| `tailscale.com/tsnet` | Host-agent joins tailnet as a Go library |
| `github.com/skip2/go-qrcode` | QR code PNG generation for invite display |
| `github.com/golang-jwt/jwt/v5` | Invite blob signing and parsing |

---

## Open Questions

1. **Credential storage encryption** — what key protects `encrypted_cred` on the guest
   side? Deferred until the broader secrets management design is settled.

---

## Implementation Plan

### Phase Dependencies

```
Phase 1 (tsnet spike) ──────────────────────────────────── GATE
     │
     ├── Phase 2 (schema) ── Phase 3 (store) ──┬── Phase 4 (authentik)
     │                                          │
     │                                          ├── Phase 5 (invite gen)
     │                                          │        │
     │                                          └── Phase 6 (accept/cred) ── Phase 7 (proxy)
     │                                                    │
     │                                               Phase 8 (revocation)
     │                                                    │
     └───────────────────────────────────── Phase 9 (UI) ─┘
```

Phases 2–4 can proceed in parallel once Phase 1 passes. Phase 9 can be stubbed against
mock API responses while backend phases are in progress.

---

### Phase 1 — Tailscale in host-agent (spike gate)

**Goal:** Prove `tsnet` works inside the Lima VM before building anything else. If tsnet
has issues running inside a rootless Podman container, the architecture needs to change.
Everything else is blocked on this.

**Files:**
- `internal/tailscale/node.go` — wraps `tsnet.Server`, exposes `Dial(ctx, network, addr)`
  and `LocalAddr() string`
- `cmd/host-agent/main.go` — start tsnet node when `BLOUD_TS_AUTHKEY` is set, log
  assigned tailnet address, skip silently if not set

#### Validation

**Fast:**
```
// internal/tailscale/node_test.go
// TestNodeSkippedWhenNoAuthKey: NewNode("") returns nil, no error
// TestNodeDialReturnsErrorWhenNil: Dial on nil node returns sentinel error

go test ./internal/tailscale/...
```

**Integration (Lima gate — blocks all other phases):**
```bash
# 1. Health still responds after tsnet starts
curl -sf http://localhost:3000/api/health | grep '"status":"ok"'

# 2. Node appears in Headscale
curl -sf -H "Authorization: Bearer <api-key>" <headscale-url>/api/v1/node \
  | jq '[.nodes[].name] | map(select(startswith("bloud-dev"))) | length > 0'
# → true

# 3. Tailnet address logged at startup
limactl shell bloud-dev journalctl --user -u bloud-host-agent -n 50 \
  | grep "tailnet address"
```

---

### Phase 2 — DB schema

**Files:** `internal/db/schema.sql`, `internal/testdb/testdb.go`

Add the `shares` and `remote_apps` tables (see Data Model above). No migration tooling
needed — `CREATE TABLE IF NOT EXISTS` means existing DBs pick them up on next startup.
Update `testdb.Schema` const to include both tables.

#### Validation

**Fast:**
```
// internal/db/schema_test.go
// TestSharesTableExists: INSERT a row, SELECT it back
// TestRemoteAppsTableExists: same for remote_apps
// TestStatusDefaultIsPending: INSERT share with no status, verify default='pending'
// TestRemoteAppStatusDefault: verify default='pending_credential'

go test ./internal/db/...
```

---

### Phase 3 — Store layer

**Files:** `internal/store/shares.go`, `internal/store/remote_apps.go`

Standard CRUD matching the existing `apps.go` pattern. Add both stores to `api.Server`
and wire in `main.go`.

```go
type ShareStore interface {
    Create(share Share) error
    GetByID(id string) (*Share, error)
    ListByApp(appID string) ([]*Share, error)
    SetGuestAddr(id, tailnetAddr string) error
    Activate(id string) error
    Revoke(id string) error
}

type RemoteAppStore interface {
    Create(app RemoteApp) error
    GetByID(shareID string) (*RemoteApp, error)
    List() ([]*RemoteApp, error)
    SetCredential(shareID string, encryptedCred []byte) error
    SetStatus(shareID, status string) error
    Delete(shareID string) error
}
```

#### Validation

**Fast:**
```
// internal/store/shares_test.go
// TestShareStore_Create: GetByID returns row with status=pending
// TestShareStore_SetGuestAddr: updates addr, other fields unchanged
// TestShareStore_Activate: status → active
// TestShareStore_Revoke: status → revoked, revoked_at set, row still readable
// TestShareStore_ListByApp: filters by app_id correctly
// TestShareStore_RejectDuplicateID: second Create with same ID → error

// internal/store/remote_apps_test.go
// TestRemoteAppStore_Create: List returns it
// TestRemoteAppStore_SetCredential: stores bytes, GetByID returns them
// TestRemoteAppStore_SetStatus: transitions work
// TestRemoteAppStore_Delete: GetByID returns nil after Delete

go test ./internal/store/...
```

---

### Phase 4 — Authentik user provisioning

**Files:** `pkg/authentik/client.go` (extend existing)

```go
func (c *Client) CreateUser(email, username, password string) error
func (c *Client) DeleteUser(username string) error
func (c *Client) SetUserPassword(username, password string) error
```

Wraps `POST /api/v3/core/users/`, `DELETE /api/v3/core/users/{id}/`, and
`POST /api/v3/core/users/{id}/set_password/`.

#### Validation

**Fast:**
```
// pkg/authentik/client_test.go (extend existing)
// TestCreateUser_Success: POST with correct body → no error
// TestCreateUser_Conflict: 400 → typed error
// TestDeleteUser_Success: DELETE → no error
// TestDeleteUser_NotFound: 404 → no error (idempotent)
// TestSetUserPassword_Success: POST set_password → no error
// TestSetUserPassword_ServerError: 500 → error

go test ./pkg/authentik/...
```

**Integration (Lima):**
```
// pkg/authentik/client_integration_test.go
//go:build integration
// TestCreateAndDeleteUser_Integration:
//   1. CreateUser "bloud-share-test@example.com"
//   2. Verify present via GET /api/v3/core/users/?search=bloud-share-test
//   3. SetUserPassword → verify login via POST /api/v3/core/auth/login/
//   4. DeleteUser → verify 404 on subsequent GET

go test -tags integration -timeout 60s ./pkg/authentik/...
```

---

### Phase 5 — Invite generation (host side)

**Files:**
- `internal/sharing/invite.go` — JWT generation and parsing
- `internal/api/sharing.go` — HTTP handlers
- `internal/api/routes.go` — add `/api/sharing/` route group

**Invite blob (JWT claims):**
```json
{
  "shareId": "uuid",
  "appId": "jellyfin",
  "appName": "Jellyfin",
  "hostLabel": "Alice's Server",
  "hostTailnetURL": "http://100.x.x.x:3000",
  "exp": 1234567890
}
```

Signed with HMAC-SHA256 using the host's existing `BLOUD_SECRET`. Single-use: the share
row moves from `pending` to `active` on first accept; subsequent uses return 409.

QR code: `github.com/skip2/go-qrcode` encodes the token string as a PNG data URL,
returned inline in the invite response.

**Endpoints:**
```
POST /api/sharing/invites       { appId } → { token, qrDataURL }
GET  /api/sharing/shares        → list with status
DELETE /api/sharing/shares/{id} → revoke
```

#### Validation

**Fast:**
```
// internal/sharing/invite_test.go
// TestGenerateToken_RoundTrip: generate, parse, all claims present and correct
// TestGenerateToken_Expiry: past exp → error
// TestGenerateToken_TamperedSignature: flipped byte → error
// TestGenerateToken_MissingClaims: missing shareId → error

// internal/api/sharing_test.go
// TestHandleCreateInvite_Success:
//   POST /api/sharing/invites { appId: "jellyfin" }
//   → 200, { token: "ey...", qrDataURL: "data:image/png;base64,..." }
//   → share row in DB with status=pending
// TestHandleCreateInvite_UnknownApp: → 404
// TestHandleCreateInvite_ReplayPrevented: same token accepted twice → 409 on second
// TestHandleListShares_Empty: → []
// TestHandleListShares_WithData: seeded share → returned in list

go test ./internal/sharing/... ./internal/api/...
```

---

### Phase 6 — Accept + credential flow

**Files:** `internal/sharing/crypto.go`, `internal/api/sharing.go` (continued)

**Guest-side endpoints:**
```
POST /api/remote-apps/accept               { token }
POST /api/remote-apps/{shareId}/credential { username, password }
GET  /api/remote-apps
DELETE /api/remote-apps/{shareId}
```

**Host-side endpoints (called by guest over tailnet, auth bypassed by loopback trust):**
```
POST /api/sharing/shares/{shareId}/accept      { guestEmail, displayName, guestTailnetURL }
POST /api/sharing/shares/{shareId}/credential  { username, password }
DELETE /api/sharing/shares/{shareId}/guest-remove
```

Both sides must already be on the same tailnet. The guest's `BLOUD_TS_AUTHKEY` is
configured independently; there is no per-guest key in the invite blob at this stage.

#### Validation

**Fast:**
```
// internal/sharing/crypto_test.go
// TestEncryptDecryptRoundtrip: encrypt → decrypt → bytes match
// TestDecryptWrongKey: → error
// TestDecryptTamperedCiphertext: → error

// internal/api/sharing_test.go (extend)
// Host-side:
// TestHandleShareAccept_ValidCall:
//   POST .../accept { guestEmail, guestTailnetURL }
//   → guest_tailnet_addr set in share row
// TestHandleShareAccept_UnknownShare: → 404
// TestHandleShareAccept_AlreadyRevoked: → 409
// TestHandleShareCredential_Success:
//   POST .../credential { username, password }
//   → mock AuthentikClient.CreateUser called with correct args
//   → share status → active
// TestHandleShareCredential_AuthentikError: → 502

// Guest-side:
// TestHandleAcceptInvite_Success:
//   POST /api/remote-apps/accept { token }
//   → mock host receives POST .../accept
//   → remote_app row created with status=pending_credential
//   → response has { shareId, appId, appName }
// TestHandleAcceptInvite_ExpiredToken: → 400
// TestHandleAcceptInvite_HostUnreachable: → 502
// TestHandleSetRemoteCredential_Success:
//   POST /api/remote-apps/{shareId}/credential { username, password }
//   → mock host receives .../credential
//   → remote_app encrypted_cred set, status=active
// TestHandleSetRemoteCredential_UnknownShare: → 404

go test ./internal/sharing/... ./internal/api/...
```

**Integration (Lima — two processes):**
```bash
# scripts/test-sharing-accept.sh
# Two host-agent instances: port 3000 (host), port 3001 (guest), same tailnet

TOKEN=$(curl -sf -X POST http://localhost:3000/api/sharing/invites \
  -H 'Content-Type: application/json' -d '{"appId":"jellyfin"}' | jq -r .token)
[ -n "$TOKEN" ] || { echo "FAIL: no token"; exit 1; }

SHARE_ID=$(curl -sf -X POST http://localhost:3001/api/remote-apps/accept \
  -H 'Content-Type: application/json' -d "{\"token\":\"$TOKEN\"}" | jq -r .shareId)
[ -n "$SHARE_ID" ] || { echo "FAIL: no shareId"; exit 1; }

curl -sf -X POST http://localhost:3001/api/remote-apps/$SHARE_ID/credential \
  -H 'Content-Type: application/json' -d '{"username":"bob","password":"hunter2"}' \
  | grep -q '"status":"active"' || { echo "FAIL: credential step failed"; exit 1; }

curl -sf -H "Authorization: Bearer $AUTHENTIK_TOKEN" \
  "http://localhost:9000/api/v3/core/users/?search=bob" \
  | jq '.results | length > 0' | grep -q true \
  || { echo "FAIL: user not in Authentik"; exit 1; }

echo "PASS"
```

---

### Phase 7 — Proxy

**Files:** `internal/api/proxy.go`, `internal/api/routes.go`

Route: `/embed/{appId}@{hostSlug}/*`

1. Look up `RemoteApp` by `appId` + `hostSlug` (derived from `host_label`)
2. Return 404 if not found, 403 if revoked, 503 if offline
3. Decrypt stored credential
4. Reverse proxy to `tailnet_addr` using a custom transport that dials via `tsnet.Dial`
   instead of the default TCP dialer, injecting `Authorization: Basic <cred>`

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
// internal/api/proxy_test.go
// TestProxy_InjectsBasicAuth:
//   httptest.Server records Authorization header
//   RemoteApp in DB → tailnetAddr=that server, cred=bob:hunter2
//   GET /embed/jellyfin@alice-server/web/index.html
//   → upstream receives Authorization: Basic <base64(bob:hunter2)>
//   → client receives upstream response body
// TestProxy_OfflineApp: status=offline → 503 JSON (not panic, not HTML)
// TestProxy_RevokedApp: status=revoked → 403
// TestProxy_UnknownApp: no RemoteApp row → 404
// TestProxy_PathPassthrough:
//   request to /embed/jellyfin@alice-server/web/index.html
//   → upstream receives GET /web/index.html (prefix stripped)
// TestProxy_CustomDialer:
//   fake tsnet Dial returns net.Conn to test server
//   → proxy uses it, confirming WireGuard path will be taken in production

go test ./internal/api/...
```

**Integration (Lima):**
```bash
# After Phase 6 integration script has run and Jellyfin is installed on host:

curl -sf http://localhost:8080/embed/jellyfin@alice-server/health \
  | grep -q '"Status":"Healthy"' || { echo "FAIL: proxy health"; exit 1; }

curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/jellyfin@alice-server/web/index.html \
  | grep -q 200 || { echo "FAIL: proxy not returning 200"; exit 1; }

# Corrupt credential → expect 401 from Jellyfin (proves injection is working)
limactl shell bloud-dev sqlite3 ~/bloud-data/bloud.db \
  "UPDATE remote_apps SET encrypted_cred=X'deadbeef' WHERE app_id='jellyfin'"
curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/jellyfin@alice-server/web/index.html \
  | grep -q 401 || { echo "FAIL: bad cred should 401"; exit 1; }

echo "PASS"
```

---

### Phase 8 — Revocation

**Files:** `internal/api/sharing.go` (complete the revocation handlers)

**Owner revokes:** deletes Authentik user, marks share revoked, notifies guest tailnet
address (failure non-fatal — guest discovers on next proxy request).

**Guest removes:** notifies host, host cleans up Authentik user + share record, guest
deletes RemoteApp row.

#### Validation

**Fast:**
```
// internal/api/sharing_test.go (extend)
// TestHandleRevokeShare_OwnerRevokes:
//   DELETE /api/sharing/shares/{shareId}
//   → mock AuthentikClient.DeleteUser called
//   → mock guest endpoint receives notify-revoked
//   → share row status=revoked
// TestHandleRevokeShare_NotificationFails:
//   guest endpoint returns 500
//   → revocation still completes (notification is best-effort)
//   → share row status=revoked
// TestHandleRevokeShare_UnknownShare: → 404
// TestHandleGuestRemove:
//   DELETE /api/remote-apps/{shareId}
//   → mock host receives guest-remove
//   → remote_app row deleted
// TestProxyAfterRevocation:
//   revoke share → GET /embed/jellyfin@alice-server/ → 403

go test ./internal/api/...
```

**Integration (Lima):**
```bash
# scripts/test-sharing-revoke.sh (runs after test-sharing-accept.sh)

curl -sf -X DELETE http://localhost:3000/api/sharing/shares/$SHARE_ID \
  | grep -q '"status":"revoked"' || { echo "FAIL: revoke failed"; exit 1; }

curl -sf -H "Authorization: Bearer $AUTHENTIK_TOKEN" \
  "http://localhost:9000/api/v3/core/users/?search=bob" \
  | jq '.results | length == 0' | grep -q true \
  || { echo "FAIL: user still in Authentik"; exit 1; }

curl -o /dev/null -sw "%{http_code}" \
  http://localhost:8080/embed/jellyfin@alice-server/web/index.html \
  | grep -q 403 || { echo "FAIL: proxy should 403 after revocation"; exit 1; }

echo "PASS"
```

---

### Phase 9 — UI

**New files:**
- `web/src/lib/components/ShareModal.svelte` — owner shares an app; shows QR code,
  copy-to-clipboard token, list of active shares with revoke buttons
- `web/src/lib/components/AcceptInviteModal.svelte` — paste/scan token, then credential
  setup form
- `web/src/lib/components/RemoteAppTile.svelte` — extends `AppTile.svelte`; shows
  "Hosted by X" badge, offline/revoked states, access credentials view

**Modified files:**
- `AppContextMenu.svelte` — add "Share" option (installed, non-system apps only)
- `+page.svelte` — fetch `/api/remote-apps`, render in dashboard grid
- `Sidebar.svelte` — "Add shared app" entry point

#### Validation

**Playwright (`web/tests/sharing.test.ts`):**
```
// TestShareFlow:
//   Right-click Jellyfin tile → Share → modal shows QR (img[src^="data:image/png"])
//   Copy token button → clipboard contains "ey..." string
//   Share list shows "No active shares"

// TestAcceptFlow:
//   (Invite token seeded via API before test)
//   Sidebar → Add shared app → paste token → credential form appears
//   Fill username + password → Submit
//   Dashboard shows Jellyfin with "Hosted by Alice's Server" badge
//   Clicking tile navigates to /embed/jellyfin@alice-server/

// TestRevokeFlow:
//   (Share seeded via API before test)
//   ShareModal → share list shows "bob@example.com — active"
//   Click Revoke → confirm → row gone, toast "Access revoked"

// TestOfflineBadge:
//   (remote_app seeded with status=offline via API before test)
//   Dashboard shows Jellyfin with offline indicator
//   Clicking it shows "App unavailable", not a broken page

npx playwright test tests/sharing.test.ts --reporter=line
```

---

## Running Tests

### After every change (fast tier)

```bash
cd services/host-agent
go test ./internal/sharing/... ./internal/store/... ./internal/api/... \
        ./internal/db/... ./internal/tailscale/... ./pkg/authentik/...
```

### Integration tier (Lima VM required)

```bash
cd services/host-agent
go test -tags integration -timeout 120s ./pkg/authentik/...
bash scripts/test-sharing-accept.sh
bash scripts/test-sharing-revoke.sh
```

### UI tier

```bash
cd services/host-agent/web
npx playwright test tests/sharing.test.ts --reporter=line
```
