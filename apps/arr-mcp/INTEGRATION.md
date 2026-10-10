# arr-mcp integration

Wires [`bardesss/arr-mcp`](https://github.com/bardesss/arr-mcp) into Bloud as one
MCP provider over the request and arr stack. The image serves MCP, `/healthz`
and a config UI from one port, and reads everything from a mounted
`/config/config.yaml` that Bloud writes.

## Credential model

Two directions, both the standard wrapper shapes.

**Inbound** is the `mcp` contract's `httpToken`. Bloud mints a bearer, writes its
`sha256:<hex>` hash into `auth.tokens`, and publishes the plaintext under
`provides.mcp.secrets.httpToken`. The listener rejects a missing or wrong bearer,
so the published credential is checked by the thing it was given.

**Outbound** is one credential per contract:

- `requestManager` (Seerr): the key Seerr generated for itself, published by
  `apps/seerr` under the same contract, plus the `default_user` account requests
  are attributed to.
- `pvr` (Radarr/Sonarr): the key each Servarr publishes under its own `pvr`
  offer.
- `mediaServer` (Jellyfin): a named API key this app mints through the bootstrap
  admin login the contract publishes, then stores privately.

## The Jellyfin mint

Lookup before create, through the single client in `apps/jellyfin/api.go`
(`EnsureAPIKey`). The key shows up in Jellyfin's Dashboard -> Security -> API
Keys under
the name `arr-mcp`, so the operator can revoke the agent without rotating the
admin password. The admin *account* is not a constant: it arrives on the
binding, because a Jellyfin Bloud booted and one the operator registered from
off-host publish different accounts.

## The config UI password

arr-mcp's config UI is the only place a human can add services or mint and revoke
tokens, so it cannot be left unclaimed: an unclaimed instance serves its setup
page to whoever reaches it first. Bloud claims it by minting a password, writing
its scrypt hash into `auth.password_hash`, and publishing the plaintext under the
`clientPassword` contract with `reveal: once` and `rotate: bloud`, the same shape
as `apps/hermes-webui`.

The UI login is username `bloud` and the revealed password. The hash format
(`scrypt$<salt>$<hash>`) matches the image's own verifier, and the salt is
derived from the password so a steady-state resync writes the same bytes instead
of a fresh salt every pass.

## Config file ownership

Bloud owns `config.yaml` outright and rewrites it on every reconciliation with
`managedfile.Write`. The image's config UI also saves over the same file, so a
service or token the
operator adds there is overwritten on the next pass. That is a first-cut limit,
not a goal: Bloud manages the services, the token and the UI password, so the UI
is only needed for things outside Bloud's catalog.

## Permission tiers

The single token Bloud mints carries `tier: read`, and the per-service
`permissions` block is left at its default of all-false. The agent can read the
stack but not write or destroy, which is the whole point of shipping a
read-defaulted provider.

## Health and readiness

`/healthz` is liveness only: it answers 200 even when the config failed
validation. The functional gate is `PostStart`, which completes the MCP
handshake with the bearer (a broken config answers 503 on `/mcp`, a missing
bearer 401) and then calls `stack_health`, which reports a service whose
credential was refused as a degraded entry.

## Icon

No `icon.png` ships yet. The upstream project's logo has not been fetched into
the tree; the dashboard falls back to a letter avatar, and the app is headless
so no tile is drawn. Add the upstream logo before this stops being true.

## Dev notes

- The image runs as a uid Bloud cannot set, so the mounted config dir is
  world-writable (`0o777`), the same treatment as `apps/seerr`.
- `ARR_MCP_PORT` is pinned to 6060 in the container spec to match the catalog
  `port` with no translation layer.
- There is deliberately no opt-in dev switch: the one thing worth switching
  (plain-http access to a service) is not a constraint here, because the
  providers are reached on the container network.
