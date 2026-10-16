# Plan: Generalize the repeated app-configurator scaffolding

> Status: implemented in the `analysis/app-code-reduction` branch, not merged.
> Written 2026-10-16 against `main` at `6719470`.
> This plan covers the code inside `apps/<name>/`. It is the other half of
> [app-add-tax.md](archive/app-add-tax.md) (the edits outside `apps/`, landed)
> and of [configurator-sdk-surface.md](archive/configurator-sdk-surface.md)
> (the `pkg/configurator` surface, landed). Those two plans reduced the cost of
> adding an app. This plan reduces the cost of the configurator itself.

## Why

Bloud ships 24 user apps and 2 system apps. The `apps/` tree holds about
17,700 lines of non-test Go. A large share of that is scaffolding that repeats
across apps. The per-app behavior is real and must stay: Home Assistant's
onboarding, Seerr's many endpoints, Jellyfin's setup wizard, Radicale's sharing
model. That logic will not shrink. The scaffolding around it will.

The proof that generalization works here is `pkg/servarr`. Radarr and Sonarr
are 62 lines each because the shared lifecycle lives in `pkg/servarr`. Prowlarr
is 852 lines because it adds the indexer-sync logic on top of the same base.
That is the shape this plan extends to the other repeated shapes.

## What repeats (measured)

The measurements below come from `grep` and `wc` over `apps/` at `6719470`.
Line counts are non-test Go.

### 1. The MCP readiness probe

Five apps probe an MCP streamable-HTTP server the same way. The probe sends an
`initialize` handshake, offers `application/json, text/event-stream`, and reads
the JSON-RPC envelope. The five files are
`apps/dav-mcp/api.go`, `apps/ha-mcp/api.go`, `apps/hindsight/api.go`,
`apps/navidrome-mcp/api.go`, and `apps/arr-mcp/api.go`. They total 700 lines.
Most of each file is the probe, not app logic.

The shared parts are byte-for-byte or nearly so:

- the `acceptStreams` constant (5 copies),
- the `initializeRequest` constant (5 copies),
- the `mcpHandshakeOK` predicate and its `jsonRPCMessage` helper (5 copies),
- the `waitServing` / `waitReady` method (5 copies, differ only in path and budget).

### 2. The mint-a-credential pattern

Six files carry the same idiom. The configurator reads a stored secret, mints a
new one on the first pass, stores it with `SetAppSecret`, and reuses it after.
The files are `apps/affine-mcp/configurator.go`,
`apps/arr-mcp/credentials.go`, `apps/dav-mcp/configurator.go`,
`apps/ha-mcp/configurator.go`, `apps/hindsight/configurator.go`, and
`apps/navidrome-mcp/configurator.go`. The same idiom appears again in
`apps/hermes-webui`, `apps/penpot/mcp.go`, and `apps/podly` for passwords.

### 3. Random token generation

Eleven files each define a `randomToken` or `randomSecret` function. The body is
four lines: read `n` bytes from `crypto/rand`, encode with
`base64.RawURLEncoding`. The files are the eleven hits of
`RawURLEncoding.EncodeToString` under `apps/`. Some mint 32 bytes, some 24.
The only difference is the byte count.

### 4. Key=value and env-file rendering

Ten files each build a config file from `key=value` lines. Each re-implements
the same rules: fixed ordering for byte-identical output, a newline guard on
values, and a generated-file header. The renderers are `renderConfig`,
`renderEnvFile`, or `renderConfigFile` in `apps/affine`, `apps/affine-mcp`,
`apps/dav-mcp`, `apps/ha-mcp`, `apps/hindsight`, `apps/immich`,
`apps/navidrome-mcp`, `apps/penpot`, `apps/podly`, and `apps/radicale`.

### 5. The public-URL wrapper

Six configurators copy `deps.Address.PrimaryBaseURL` into a private
`ssoBaseURL` field and wrap it with a thin `appExternalURL()` method. That
method just calls `configurator.AppExternalURL(c.ssoBaseURL, appName)`. The six
are AFFiNE, Hermes, hermes-webui, Paperless-ngx, Penpot, and Vaultwarden. The
helper already exists in `pkg/configurator/urls.go`. The field, the constructor
line, and the wrapper method are the copy.

### 6. The constructor boilerplate

Nearly every `NewConfigurator(port int, deps configurator.Deps)` repeats the
same steps: default the port to `defaultPort`, default a nil logger, stamp the
logger with the app name, copy `deps.Secrets`, and build the typed API client
when `deps.HTTP` is not nil. This is about 15 lines per app, times 24 apps.

### 7. Single-provider binding pickers

Ten typed helpers repeat the same loop: guard a nil state, iterate one slice of
`state.Integrations`, skip entries whose secret is not `Published()`, and return
the first. Examples are `apps/affine/inference.go`,
`apps/affine-mcp/configurator.go`, `apps/arr-mcp/configurator.go`,
`apps/seerr/configurator.go`, and `apps/hermes/configurator.go`.

### 8. Native-OIDC config rendering and first-admin bootstrap

This is the hardest to generalize. AFFiNE, Immich, Paperless-ngx, Penpot, and
Vaultwarden each render their own OIDC config in a different format. Navidrome,
Paperless-ngx, AFFiNE, and Home Assistant each create a first admin and log in
through the app's own API. The formats and APIs differ, so the content must
stay per app. Only the write-and-report-change skeleton is shared. See the
"what not to do" section.

## The proposals

Ordered by lines saved times ease. Each is independently shippable, matching the
style of the two landed plans.

### P1. A `pkg/mcp` package: the streamable-HTTP probe

Move the handshake probe into the SDK. The package exports the two constants,
the envelope check, and a probe helper. Sketch:

```go
package mcp

const AcceptStreams = "application/json, text/event-stream"

// HandshakeOK reports whether a response is a successful MCP initialize result.
func HandshakeOK(status int, body []byte) bool

// Probe performs the initialize handshake against path and waits for
// HandshakeOK within the budget. bearer may be empty.
func Probe(ctx context.Context, cl *appclient.Client, path, bearer string, within time.Duration) error
```

Each of the five `api.go` files then keeps only its app-specific facts: the
path, the budget, and the one comment that says why the probe is the honest
readiness signal for that app. The five copies of the constants, the envelope
check, and the handshake method collapse to one.

Estimated saving: about 400 of the 700 lines across the five files.

### P2. Credential and token helpers in the SDK

Add to `pkg/configurator` (or a small `pkg/secretutil`):

```go
// RandomToken returns n bytes of entropy as URL-safe base64.
func RandomToken(n int) (string, error)

// EnsureSecret returns the stored secret for app/key, or mints one of size
// bytes and stores it. A nil provider returns "" without error, matching the
// CLI/test contract the configurators already assume.
func EnsureSecret(secrets AppSecretsProvider, app, key string, size int) (string, error)
```

`EnsureSecret` folds the read-back, the mint, the store, and the nil guard into
one call. The six bearer-mint functions and the eleven `randomToken` functions
then become one line each. The `GenerateAppAdminPassword` plus
`SetAppSecret(app, "password", ...)` pair (AFFiNE, Podly, Seerr, the Home
Assistant companion) can get a third helper `PublishAdminPassword`, because it
is the same two calls everywhere.

Estimated saving: about 180 lines, plus one source for the idiom.

### P3. Put the base URL on `AppState`

`AppState` already carries `DataPath`, `BloudDataPath`, and the bindings. Add
the resolved base URL, and a method:

```go
type AppState struct {
    // ...
    primaryBaseURL func() string
}

// AppExternalURL returns the app's public URL, derived the same way the
// routes are. Safe to call with a nil base URL.
func (s *AppState) AppExternalURL(appName string) string
```

The orchestrator fills `primaryBaseURL` from the same source it builds `Deps`
from. Configurators then call `state.AppExternalURL(appName)` inside PreStart
or PostStart and drop the `ssoBaseURL` field, the constructor assignment, and
the wrapper method.

Estimated saving: about 10 lines per app times 6 apps, plus less constructor
plumbing for every future app that needs its public URL.

### P4. A declarative "generated file" base (the destination)

Most configurators are the same five steps: compute values from `AppState` and
secrets, render one config file, write it with `managedfile.Write`, report
`MustRestart` only on change, and wait for readiness with `appclient`. The MCP
wrappers are the purest case: mint a bearer, resolve one binding, render an env
file, write it, probe `/mcp`.

A `pkg/generatedfile` (working name) type can own steps 2 through 4. The app
supplies the path, the mode, a `render func(AppState) (string, error)`, and a
`restartReason` string. The base implements the `managedfile.Write`, the
changed check, and the `PreStartResult`. The app's `render` stays typed, so the
stringly typed trap from `configurator-sdk-surface.md` is not reintroduced.

For the wrapper shape specifically, a `pkg/mcpwrapper` type can own the whole
skeleton. A new wrapper then declares: the config path, the bearer secret key,
the provider binding to read, and the render function. AFFiNE-mcp, dav-mcp,
navidrome-mcp, and the server half of ha-mcp and hindsight shrink to a small
config struct each.

This is the largest saving but also the largest design. It wants its own plan
with the type shape written out. P1 through P3 are the prerequisites: they
extract the pieces P4 composes.

Estimated saving: one to two thousand lines across the five wrappers, and the
same skeleton removed for every future wrapper.

### P5. A binding-picker helper

The loop in proposal 7 is a small generic helper:

```go
package bindings

// First returns the first element of xs for which ready reports true.
func First[T any](xs []T, ready func(T) bool) (T, bool)
```

Each typed picker becomes `bindings.First(state.Integrations.AppAPIs, func(b AppAPIBinding) bool { ... })`.
The win is small and mostly removes the nil-state guard and the loop from ten
sites. It is optional and can ride with P2.

### P6. A configurator base struct

A `configurator.Base` embedding can own the port default, the logger default,
the app-name stamp, and the secrets copy. Apps embed it and keep their own
`Name()`, `Port()`, and API client. This removes the constructor boilerplate
from proposal 6. It is the smallest win and the most churn, so it ranks last.
Do it only if P4 does not already absorb it.

## What shipped in this branch

P1, P2, P3, and P4 shipped as written. P1 adds `pkg/mcp` and migrated the five
MCP probes. P2 adds `RandomToken` and `EnsureSecret` to `pkg/configurator` and
migrated every token and bearer mint. P3 moved the base URL onto `AppState` and
migrated the six public-URL readers. P4 adds `pkg/mcpwrapper` and migrated the
three pure wrappers: affine-mcp, dav-mcp, and navidrome-mcp.

One deviation from the proposal: the server halves of ha-mcp and hindsight stay
hand-written. Each has a second gate node that shares the credentials and writes
its own nginx config, which is not the single-container shape `mcpwrapper` owns.
They still got P1 and P2.

P5 and P6 did not ship. They are the optional, low-value tails of the plan.

## What NOT to do

These limits keep the plan aligned with the two landed plans.

- Do not make the bindings generic on the consumer side. The typed payloads
  stay. `configurator-sdk-surface.md` already rejected
  `type Binding struct { Values map[string]string }`. P5 only removes the loop,
  not the type.
- Do not touch the registration mechanism. The blank import, the
  `registration.go` `init`, and `NodeNames()` are a deliberate rename guard.
  `configurator-sdk-surface.md` says to leave them alone.
- Do not force app-specific behavior into a framework. The first-admin
  bootstrap and the per-format OIDC render stay in the app. Only the
  write-and-report skeleton moves.
- Do not regress the two invariants every configurator obeys: idempotence, and
  a byte-identical no-op on a steady-state resync. P4 must keep the fixed
  ordering and the changed check, or the self-healing pass will recreate
  containers every 60 seconds.

## Sequencing

| PR | Change | Depends on |
|---|---|---|
| 1 | P1: `pkg/mcp` probe | nothing |
| 2 | P2: `RandomToken`, `EnsureSecret`, `PublishAdminPassword` | nothing |
| 3 | P3: base URL on `AppState` | nothing |
| 4 | P5: `bindings.First` | nothing |
| 5 | P4: `pkg/generatedfile`, then `pkg/mcpwrapper` | 1, 2, 3 |
| 6 | P6: `configurator.Base`, only if P4 left constructor code behind | 4 |

PRs 1 through 4 are independent and each shippable alone. P4 is the
destination.

## Verification

- `cd services/host-agent && go test ./...` and `cd apps && go test ./...`
  after every step.
- `cd apps && go test ./... -run TestConformance` pins the nil-safe `Deps`
  contract and the empty-means-not-ready rule, so a helper that breaks either
  fails there.
- The behavior tests in `apps/*/configurator_test.go` already pin byte-identical
  output for the renderers. P4 must keep those green without edits, which is
  the proof that the skeleton moved and the content did not.
- `npm run check:no-emdash` and `npm run check:docs-links` for the doc edits.

## How to know it worked

| Metric | Now | Target |
|---|---|---|
| Non-test Go under `apps/` | about 17,700 lines | under 14,000 after P4 |
| Files with the MCP handshake constants | 5 | 1 (`pkg/mcp`) |
| Files with a `randomToken`/`randomSecret` function | 11 | 0 |
| Files with a bearer-mint function | 6 | 0 (one call to `EnsureSecret`) |
| Files with an `appExternalURL` wrapper method | 6 | 0 |
| New MCP wrapper cost | a configurator, an `api.go`, a credentials file, and a registration | one metadata file plus a small config struct |

If a new app still needs a hand-written readiness probe or a hand-written
credential mint, this plan is incomplete. Name the missing derivation here.
