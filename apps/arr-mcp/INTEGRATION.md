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

- `requestManager` (Seerr): the Jellyfin login Seerr's admin was created from,
  published by `apps/seerr` under the same contract. This app logs into Seerr
  with that pair (`apps/seerr`'s `DeriveAPIKey`) and derives Seerr's own
  `main.apiKey`, which is what it then writes into `config.yaml` as
  `api_key`; the username becomes `default_user`. Seerr has no token-minting
  endpoint, so deriving its key is the only way to hand it over without asking
  the operator to copy it out of the UI.
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

arr-mcp gates a write twice, and refuses unless both gates allow it: the tier of
the token that presented the bearer, and a `permissions` block on the service
the write targets. The image defaults that block to all-false, so raising only
the token tier would still refuse every write; the refusal names the YAML key to
set, which is the only clue a harness gets.

Bloud writes both. The single token it mints carries `tier: write`, and every
service it wires gets `safe_write: true, destructive: false`. So the agent can
file and approve a request, add a film, start a search, monitor, pause a
download, and generally run the stack; it cannot delete media, delete a request
record, or clear a queue. That is the line arr-mcp itself draws: a `safe` write
is one the service can undo, a `destructive` one loses something.

The tier is one constant (`tokenTier` in `configurator.go`), and every write the
agent makes lands in the image's own audit table regardless of tier. There is
deliberately no environment switch for it: a default that can be flipped by an
env var nobody remembers setting is a default nobody can reason about.

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
