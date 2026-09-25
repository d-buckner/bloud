# Contributing Apps to Bloud

Welcome. Adding an app is the most valuable contribution you can make to Bloud.
This guide gives you the full picture: how the pieces fit together, what each
file does, and where to find a working example close to your app's shape.

If you only skim one thing, read [How it all fits together](#how-it-all-fits-together) and then open the [reference app](#reference-apps) closest to yours. Most
questions answer themselves once you see a real app side by side with this guide.

For the broader component overview, see
[docs/architecture/overview.md](../architecture/overview.md).

## How it all fits together

A Bloud app contribution has two halves:

1. **A declaration** (`metadata.yaml`). This is what the app *is*: its containers,
   ports, volumes, databases, and how it wants SSO. The catalog reads this and
   the orchestrator builds the dependency graph from it.
2. **A configurator** (Go code). The runtime glue that can't be expressed
   statically: talking to the app's own API, writing config files it expects,
   completing any setup wizards.

The orchestrator is the engine that turns both into reality. It continuously
compares the desired state (your declaration) with the actual state and runs a
cycle per app node to close the gap:

```
INITIALIZING → PRESTART → STARTING → POSTSTART → RUNNING
```

The key property to internalize: **every cycle is a fresh start.** The same
`PreStart`/`PostStart` your app runs during install also runs after a crash
restart and after a host reboot. That is what makes Bloud self-healing. Your
configurator is the thing that re-establishes integrations (API keys, OIDC
clients, LDAP wiring) without anyone touching them again. Design each method so
that running it twice, at any moment, is harmless: check whether the thing is
already in place, act only when it isn't, and report whether you changed
anything.

Readiness is declarative too: the `healthCheck:` block you write in
`metadata.yaml` is what the orchestrator waits for between PreStart and
PostStart, so your `PostStart` only ever runs against a live container.

## App Structure

Each app lives in `apps/<name>/`:

```
apps/your-app/
  metadata.yaml     # identity, port, integrations, container spec, SSO
  configurator.go   # NodeLifecycle implementation only
  registration.go   # init() -> configurator.MustRegisterFactory
  api.go            # re-usable client for communicating with the app over the network
  icon.png          # 256x256 PNG, transparent background
  INTEGRATION.md    # integration notes for whoever maintains this app
```

Keep `configurator.go` focused on the lifecycle wiring; helper code has its
own home: see [Where helper code goes](#where-helper-code-goes).

## Step 1: metadata.yaml

Declares what the app needs and how to run it. The catalog loads this at
startup, so most of your app's behavior needs no code at all.

```yaml
name: your-app
displayName: Your App
description: What it does in one sentence
category: media            # media, productivity, security, infrastructure
port: 8080

integrations:
  database:
    required: true
    compatible: [{ app: postgres, default: true }]
  sso:
    required: true
    compatible: [{ app: authentik }]

# What your app offers to the consumers that integrate with it (optional),
# keyed by the contract names those consumers put in their `integrations:`.
# Credentials listed here are published to the host secret store; values travel
# in this file. See "Publishing for other apps" below.
provides:
  pvr:
    secrets: [apiKey]

sso:
  strategy: native-oidc    # native-oidc, ldap, forward-auth, none
  # callbackPath: /oauth/callback   # native-oidc: where the app receives the code
  # scopes: [offline_access]        # native-oidc: scopes beyond openid/profile/email
  # accessTokenMinutes: 60          # native-oidc: access token lifetime (default 5)
  # loopbackIssuer: true   # only for OIDC clients that reject a non-loopback
  #                        # http issuer (see apps/hermes)

containers:
  - name: apps-your-app
    image: someorg/someimage:1.2.3   # pin the version
    network: apps-net
    restartPolicy: always
    environment:
      TZ: Etc/UTC
    ports:
      - host: 8080
        container: 8080
    volumes:
      - source: "{{appDataDir}}/config"
        destination: /config
    healthCheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:8080/health"]
      interval: 5
      timeout: 10
      retries: 12
```

If your app has no integrations: `integrations: {}`.

Template variables available in `containers[].environment` and
`containers[].volumes`:

- `{{appDataDir}}`: your app's own data directory, kept private to it
- `{{dataDir}}`: the shared Bloud data directory (for things like media libraries)
- `{{postgresPassword}}`: a per-app PostgreSQL password, generated and
  stored by the host for apps that bundle their own postgres container

A few friendly defaults worth knowing:

- Apps that need databases bring their own postgres/redis containers in
  `containers:`. Every app gets an isolated database. Declaring them as
  separate nodes also means the graph orders them: postgres comes up before
  your app, automatically.
- `isSystem: true` hides an app from the user-facing catalog (used for
  infrastructure like traefik and authentik).
- The `healthCheck` numbers are seconds. Pick a check the image can actually
  run. Many slim images lack curl, but their own runtime works (immich uses
  `node -e ...fetch`, home assistant uses `python3`); see the reference apps.
- The full field reference lives in
  `services/host-agent/internal/catalog/models.go`. It is the source of
  truth if this guide ever drifts.

### Pin every image to a specific version

`npm run check:image-pins` fails the build when a container image names a
rolling channel instead of a version. A rolling tag is republished upstream, so
`:release` today and `:release` next month are different bytes: a crash stops
being reproducible, a version bump stops being reviewable, and one bad upstream
push lands on every Bloud install at once.

Pinned means a version tag in whatever shape that registry uses (`1.2.3`, `v3.4`,
`7-alpine`, `pg16`, `2.6.5.5623-ls161`), or an `@sha256:` digest. Rejected: no
tag at all (the runtime reads an untagged image as `:latest`), `:latest`, and the
rolling channels (`stable`, `release`, `staging`, `edge`, `nightly`, `main`,
`dev`, `canary`, `beta`, and prefixed variants like `release-cuda`).

If you inherited a floating tag, ask the image which version it is before you
write the pin:

```bash
podman pull --quiet ghcr.io/immich-app/immich-server:release
podman inspect ghcr.io/immich-app/immich-server:release \
  --format '{{ index .Config.Labels "org.opencontainers.image.version" }}'
# -> v3.2.2, which is what apps/immich pins today
```

If an image genuinely has to float (a VPN client that must track its own network
is the live example), add it to `EXCEPTIONS` in `scripts/pinned-images.mjs` with
the reason. The check prints every accepted exception on every run, and fails an
exception that matches nothing, so an exception cannot quietly outlive its
justification.

## Step 2: configurator.go

This is where you do the work that a static container definition can't: create
config files the app expects, call its API to finish setup, register OIDC
clients, import LDAP settings.

Implement `configurator.NodeLifecycle`'s three methods (teardown is optional,
see below):

```go
package yourapp

import (
    "context"

    "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

type Configurator struct {
    port int
}

func NewConfigurator(port int, deps configurator.Deps) *Configurator {
    if port == 0 {
        port = 8080 // the registration passes 0; fall back to the app default
    }
    return &Configurator{port: port}
}

func (c *Configurator) Name() string { return "apps-your-app" }

// PreStart runs before the container starts: directories, config files,
// certificates. Return changed=true when you modified a file the container
// reads at boot; that tells the orchestrator to (re)start the container so
// it picks your changes up.
func (c *Configurator) PreStart(ctx context.Context, state *configurator.AppState) (bool, error) {
    return false, nil
}

// PostStart runs after the container is healthy: API calls, integrations,
// runtime setup. It runs on every reconciliation, so structure it as
// check-then-act: verify each desired setting via the app's API, apply only
// what is missing, and be a clean no-op when everything is already in place.
//
// The framework gives you a generous budget (PostStartBudget, default 150 s)
// and passes an already-bounded context: use it directly for all your work,
// and it will cancel cleanly if the host shuts down. A run interrupted by
// shutdown is simply re-converged on the next start, so nothing is lost and
// there is no need to detach or background long work.
func (c *Configurator) PostStart(ctx context.Context, state *configurator.AppState) error {
    return nil
}
```

Teardown is the orchestrator's job: it removes the app's containers and, on a
clear-data uninstall, the app data directory. A configurator only implements
`configurator.Remover` (`Remove(ctx, state, clearData bool) error`) when it owns
teardown the orchestrator cannot express; most apps do not. A `PostStart` that
returns an error is terminal for the node, so resolve transient conditions
(waits, retries) inside it and return an error only for a real fault.

Readiness comes from the `healthCheck:` block you declared in `metadata.yaml`,
which the orchestrator enforces between PreStart and PostStart. There is no
`HealthCheck` method to implement.

### Making HTTP calls: `pkg/appclient`

Talking to your app's HTTP API is the core of most configurators, so the host
gives you a purpose-built client for it. Build one in your constructor via the
injected factory (`deps.HTTP`) and hold it. You get connection pooling, a
sensible retry policy, and the shared logging for free, consistent with every
other app:

```go
import "codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"

type Configurator struct {
    api *appclient.Client
}

func NewConfigurator(port int, deps configurator.Deps) *Configurator {
    return &Configurator{
        api: deps.HTTP.New(appclient.Spec{
            Name:    "your-app",
            BaseURL: fmt.Sprintf("http://localhost:%d", port),
        }),
    }
}
```

A `Call` is started with a verb (`GET`/`POST`/`PUT`/`PATCH`/`DELETE`) and
ended with exactly one terminal:

| Terminal | Use |
|---|---|
| `.Do(ctx)` | fire the request, return the raw body `([]byte, error)` |
| `.DoInto(ctx, &out)` | decode a JSON response into `out` |
| `.Ensure(ctx)` | create-or-verify an idempotent resource (`(bool, error)`) |
| `.Wait(ctx)` | poll a readiness predicate until ready or the deadline |
| `.Exec(ctx)` | fire for the side effect only; the body is discarded (`error`) |
| `.Stream(ctx, consume)` | hand a successful (2xx) body to `consume(io.Reader)` for downloads/hashing without buffering |

Status handling is **declarative**: describe what each status code *means* for
your call instead of parsing bodies by hand. Attach the outcome contract before
the terminal and the client does the right thing for each case:

```go
// 200 or 204 is success; a 409 "already exists" is treated as done.
err := c.api.POST("/things").JSON(payload).
    OK(http.StatusOK, http.StatusNoContent).
    AlreadyDone(http.StatusConflict).
    Do(ctx)
```

That `.AlreadyDone(...)` pattern pairs beautifully with the idempotent-cycle
design: on the first reconciliation it creates, on every one after that it
reports "already done" without treating the 409 as an error.

Readiness polling is built in, so waiting for a service to come up is one
declaration:

```go
// Poll until the OIDC discovery document answers with an "issuer".
err := c.api.GET("/.well-known/openid-configuration").
    Ready(appclient.JSONHas("issuer")).
    Interval(2 * time.Second).
    Wait(ctx)
```

Handy declared modifiers as you need them:

- `AlreadyDoneFunc(pred)`: for APIs with no distinct "already exists" status
  code; match on the body instead.
- `Stable(n)`: require n consecutive good polls before a wait returns,
  useful when a value settles slowly or oscillates during boot.
- `TolerateFailures()`: once a good read has been observed, let a later
  timeout land non-fatally (fall through with the last good value).
- `Anonymous()`: skip auth on a single call (e.g. the login request that
  obtains the token).
- `RetryStatus(...)` / `WithRetry(policy)`: tune retry behavior per call.

Auth (`Authorization: Bearer …`, custom header formats, and 401 token
refresh) is a `TokenSpec` configured once on the client rather than
per-call boilerplate. `apps/jellyfin/api.go` and `apps/homeassistant/api.go`
show the real shapes.

The repo routes all app HTTP through `appclient`: a `forbidigo` rule in
`.golangci.yml` (run by `npm run lint:go`) fails on raw `net/http` in
`apps/**/*.go`. The same rule requires `pkg/managedfile.Write` for generated
files and `Deps.Exec`/`Deps.RestartContainer` for commands inside containers.
If a call genuinely can't use the sanctioned helper, mark that one line
`//nolint:forbidigo // reason`, so the exception sits next to the code.

### Pinned remote assets and provenance

Some apps need a file fetched from upstream at install time: a plugin, a
theme, a bundled binary. Use the injected asset installer
(`deps.Assets.Install`) and you get a hardened path for it: download with
retry, sha256 verification, and an atomic commit into a content-addressed
cache under `BLOUD_DATA_DIR`. Re-installs reuse the cache (no re-download),
and a digest mismatch refuses the file rather than installing it, so your
users always get exactly the artifact you verified:

```go
changed, err := c.assets.Install(ctx, appasset.Asset{
    Name:   "your-plugin",
    Dest:   targetDir,
    Source: appasset.URL("https://upstream.example/release.zip"),
    Kind:   appasset.Zip,
    SHA256: "…expected digest…",
})
```

Record the asset's provenance in your `INTEGRATION.md` under a **Verified
constants** section. Future-you (or the next maintainer) will be grateful:

- upstream URL
- version (tag or commit)
- sha256
- verification date
- the command used, e.g. `curl -sL <url> | sha256sum`

This makes re-verification a one-liner when you bump the plugin to a new
release. `apps/homeassistant/INTEGRATION.md` ("Verified constants") is the
worked example.

### AppState

The orchestrator hands each configurator the resolved integration outputs, so
you never have to discover provider details yourself:

| Field | Description |
|---|---|
| `state.DataPath` | App data dir (`~/bloud-data/your-app`) |
| `state.BloudDataPath` | Shared data dir (`~/bloud-data`) |
| `state.SSOEnabled` | Whether SSO integration is active for this app |
| `state.LDAP` | Typed LDAP output (host, port, baseDN, bindUser, bindPassword); populated for `sso.strategy: ldap`, nil otherwise |
| `state.OIDC` | Typed native-oidc output (`ClientID`, `ClientSecret`, `IssuerURL`, `RedirectURI`); populated for `sso.strategy: native-oidc`, nil otherwise |
| `state.Integrations` | Resolved providers per contract, one typed slice each, see below |

Both provider outputs are guaranteed complete when non-nil: if `SSOEnabled`
is true and your strategy is `ldap`, `state.LDAP` has everything you need;
same for `state.OIDC` with `native-oidc`.

### Integrating with another app

An app that wires itself to a *catalog* provider (anything you declare under
`integrations:`) reads that provider from `state.Integrations`, which holds one
typed slice per contract. It also declares which of the contract's credentials it
actually reads:

```yaml
integrations:
  pvr:
    required: false
    multi: true
    requires: [apiKey]        # only this is resolved into the binding
    compatible:
      - app: sonarr
      - app: radarr
```

```go
for _, pvr := range state.Integrations.PVRs {
    if !pvr.Installed {
        // Not installed (or uninstalled): prune any entry you wrote for it.
        continue
    }
    host, port := pvr.Node, pvr.Port   // "apps-sonarr", 8989
    // pvr.BaseURL  ("http://apps-sonarr:8989") is what the APP stores.
    // pvr.LocalURL ("http://localhost:8989") is how your configurator
    // reaches the same provider from the host.
    // pvr.APIKey is the key the provider published for this contract.
}
```

| Slice | Payload beyond the address |
|---|---|
| `PVRs` (`pvr`) | `APIKey`, the key the provider's API authenticates with |
| `MediaServers` (`mediaServer`) | `AdminPassword`, the provider's bootstrap admin password |
| `DownloadClients` (`downloadClient`) | nothing: the consumer stores the address |
| `SSO` (`sso`) | `APIToken`, to read the provider's users |
| `MCPServers` (`mcp`) | `ServerName`, `URL`, `Token` |

Every binding embeds `ProviderRef`, the part that is the same for all of them:

| Field | Description |
|---|---|
| `App` | The provider's catalog id |
| `Installed` | Whether it is installed this pass (the same condition as the dependency edge). `false` is what a prune path keys off |
| `Node` | Its container name on the app network, `apps-<id>` |
| `Port` | Its published port, from its metadata |
| `BaseURL` / `LocalURL` | `http://<Node>:<Port>` (what your app stores) and `http://localhost:<Port>` (what your configurator calls) |

A contract holds **one binding per provider**: for an optional contract that is
every declared compatible app in the catalog, and for a required contract it is
the provider chosen at install time. `Installed` distinguishes the ones that are
actually there, and a payload the provider has not published yet is empty, which
means "not ready", never "use this empty credential".

`requires` is what keeps a payload least privilege: declaring a contract gets an
app the provider's address, and nothing more. Only the credentials you list are
resolved, so an app that integrates with the identity provider to authenticate
its users does not receive that provider's admin API token unless it says it
reads it. A name the contract does not carry fails the catalog load, and so does
a `requires` against a contract the framework does not know.

That also gives an empty payload field two causes, and they are worth telling
apart before you debug a provider: it has not published the value yet (wait), or
you did not declare it in `requires` (fix your metadata).

Two rules follow from this, and they are not stylistic:

- **Never read or write another app's files.** A provider's data directory,
  its config files and their internal format are private to it. If you need
  something a provider owns, it publishes it (below); if it owns nothing you
  need, use its API.
- **Never probe a provider's port to find out whether it is installed.**
  `Installed` answers that, and it is the same answer the dependency graph
  used to order your app after the provider. A probe cannot tell "not
  installed" from "restarting", so it makes you prune wiring that is still
  wanted. Probe only if you need a *readiness* signal beyond `Installed`; then
  treat failure as transient, never as removal.

### Publishing for other apps

An app's offer is declared per contract, using the same contract names consumers
put in their `integrations:`. Two kinds of things travel that way.

**Credentials.** A name in `secrets` is published to the host secret store:

```yaml
provides:
  pvr:
    secrets: [apiKey]
```

```go
if c.secrets != nil { // nil in degraded contexts: skip, don't fail
    if err := c.secrets.SetAppSecret(appName, "apiKey", key); err != nil {
        return err
    }
}
```

A consumer of `pvr` that declares `requires: [apiKey]` receives it as
`PVRBinding.APIKey`. Only the names you declare are offered, and a name your
contract does not define fails the catalog load, so an offer can never carry a
credential no consumer could ask for. Anything else the app stores stays
private. `SetAppSecret`
is idempotent (re-publishing an unchanged value does not rewrite the store), so
calling it from `PreStart` on every reconciliation is fine. A published
credential never reaches a container's environment; the generated `<app>.env`
files carry only the host's own per-app values.

A value the *host* already generates needs no publishing code: declaring it is
enough, because the host is the one that stored it (jellyfin declares
`adminPassword`, the password `GenerateAppAdminPassword` handed it).

**Endpoint facts.** Static values travel in the metadata, so nothing has to be
published at runtime. An MCP server declares where it serves and what it calls
itself:

```yaml
port: 3011
provides:
  mcp:
    secrets: [httpToken]
    values: {path: /mcp, serverName: affine}
```

An agent app declaring the `mcp` contract receives `ServerName` and a ready
`URL` (`http://apps-affine-mcp:3011/mcp`, the same resolved address as
`BaseURL`) plus the token, and registers it through its own API.

**What a contract requires is defined once**, in
`internal/catalog/contracts.go`: its name, the secret names a provider must
publish, and the values it must declare. A declaration that does not match fails
the catalog load, so mistakes surface at startup rather than as a binding that is
quietly half-empty. The orchestrator reads the required secret names from there
too, which is why no consumer ever writes a secret name.

Adding a **provider of an existing contract** is metadata only. Adding a new
contract is three small edits: an entry in that registry, a payload type in
`pkg/configurator`, and one arm in the orchestrator's `bindContract`. Do not
invent a private convention between two apps for something a contract can carry.

### Register your configurator (self-registering)

Create `apps/your-app/registration.go`. The package registers a factory in
`init()`; the host-agent registry instantiates it lazily on the first lookup
of the node, so your configurator is only built when the app is actually
reconciled:

```go
package yourapp

import "codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"

func init() {
    configurator.MustRegisterFactory("apps-your-app", func(deps configurator.Deps) configurator.NodeLifecycle {
        // Pass the whole Deps; NewConfigurator reads what it needs from it.
        return NewConfigurator(0, deps)
    })
}
```

`configurator.Deps` carries the host-side inputs a factory may need: `Logger`,
`Secrets`, a `PrimaryBaseURL func()`, `TraefikPort`, a `RestartContainer`
callback, an `Exec` callback (run a command inside a container and read its
output), `HTTP` (the `ClientFactory` for app HTTP calls, see above), and
`Assets` (the `appasset.Installer` for static/downloaded files).

Then add your app to **`apps/registry.go`**, the single place that lists the
catalog. Two edits, both in the `apps/` module:

```go
import (
    _ "codeberg.org/d-buckner/bloud/apps/your-app"
)

func NodeNames() []string {
    return []string{
        // ...
        "apps-your-app",
    }
}
```

That's the whole registration story: you never touch host-agent's
`internal/appconfig/register.go`, which belongs to the system apps (Traefik,
Authentik) registered there as lazy factories.

There is a safety net on your side too: `TestRegisterAll` in
`apps/registry_test.go` asserts every `NodeNames()` entry has a registered
factory, so a typo or a forgotten `registration.go` is caught by
`go test ./...` while you're still working, not during someone's install.

## Where helper code goes

A configurator grows: a typed client for the app's own HTTP API, a config-file
builder, parsers, fixtures. Three tiers decide where each piece lives:

| Helper kind | Goes in |
|---|---|
| Lifecycle flow (`PreStart`/`PostStart` steps, wizard logic) | `apps/<name>/` top level, same package as the configurator |
| App-specific reusable helper (own-API client, config builder, parser, fixtures) | `apps/<name>/lib/` |
| Needed by 2+ apps, **or** by host-agent itself | `services/host-agent/pkg/` (existing: `xmlutil`, `slug`, `authentik`) |

`lib/` is an ordinary Go sub-package (`package lib`), imported as
`bloud/apps/<name>/lib`. Two helpful constraints keep it clean:

- Don't import the parent app package from `lib/` (that would be an import
  cycle). Pass what you need in as arguments.
- `lib/` can't import `services/host-agent/internal/` by design; the Go
  compiler enforces it. If a helper needs an `internal/` type, it belongs in
  the parent package.

A `lib/` package that takes `(baseURL, token, logger)` is testable on its
own; one that reaches for host-agent state is not.

New `lib/` files carry the standard two-line SPDX header, same as everything
else (`npm run license:check` will remind you; `npm run license:fix` stamps
it).

Migration is opportunistic: existing apps are not being reshaped in one go.
New apps should start in this shape, and when a fat `configurator.go` gets
split, the non-lifecycle helpers move into `lib/` as part of that work.

## Step 3: Test

### The conformance harness covers you before you write anything

You do not opt in to the conformance harness. The moment your app registers a
node, `apps/conformance_test.go` picks it up and holds it to the same six
checks as every other app:

| Check | What it proves |
|---|---|
| `name_matches_the_registered_node` | `Name()` returns the node you registered under |
| `prestart_is_offline` | `PreStart` opens no socket. It is the filesystem-only phase |
| `prestart_is_idempotent` | A second `PreStart` asks for no recreate. It must converge |
| `survives_empty_deps` | A zero-valued `Deps` does not panic |
| `teardown_is_not_a_lie` | If you implement `Remover`, it actually does something |
| `port_matches_metadata` | Your constructor's default port equals `metadata.yaml`'s `port` |

Run them with `cd apps && go test -run TestConformance ./...`.

Two things this catches that per-app unit tests reliably miss, both found in
practice:

- **A version comparison that never matches.** Home Assistant compared the
  `hass-oidc-auth` manifest against `"v1.2.1"` while the shipped manifest
  reports `"1.2.1"`. The skip never fired, so every reconciliation pass
  re-downloaded the component and recreated the container. The app's own test
  fixture carried the same wrong version as the code, so the test was green
  and the bug was live.
- **A port default that drifted from metadata.** Two were wrong when the
  harness first checked them against the catalog instead of against memory.

If your app legitimately needs the network in `PreStart`, or needs to seed a
file its own installer short-circuits on, the harness has a `Preseed` hook and
an explicit network allowance. Declare them in your table row rather than
exempting the check wholesale; Home Assistant uses `Preseed` so its pinned
asset install does not reach the network.

### Integration and e2e

Add integration test assertions under
`services/host-agent/internal/e2e/`, which is split per scenario
(`system_apps_test.go`, `jellyfin_test.go`, `affine_test.go`,
`crash_recovery_test.go`, `uninstall_test.go`, …). Add a new
`<yourapp>_test.go` for your app. Shared harness helpers (env resolution,
`waitHTTP`, `getInstalledApps`, `postJSON`, `TestMain`) live in
`e2e_test.go`; reuse them rather than writing your own.

Every file in this package is build-tag gated, so start the file with the tag
to make sure it runs with the integration tier:

```go
//go:build integration
```

Tests run against real services on the selected runtime (Lima/QEMU VM or
native host).

Test the behavioral outcome, not config values: verify through your app's own
API that the integration actually took effect:

```go
func TestYourApp_PostStartConfiguresCorrectly(t *testing.T) {
    // Install the app, then verify via its own API that integration took effect
}
```

Run with: `./bloud validate --tier integration`

Also add:

- A user-journey Playwright spec in `e2e/tests/your-app.spec.ts` (user flows
  go through the public port `http://localhost:8080`; API helpers use the
  internal API at `BLOUD_API_URL` / `:3000`). Wire it up with
  `./bloud e2e app` + `BLOUD_E2E_APP=your-app`.
  Use `describeApp` from `e2e/lib/app-suite.ts`: it is a real
  `test.describe` that also provides one shared authenticated page
  (`app.page`) and serial execution, so a red run points you straight at the
stage that broke. Write one test case per observable behavior: converge →
  catalog → home tile → open → sign-in (`openAppFromHome` opens the app
  popup; only the sign-in body is app-specific). See
  `e2e/tests/jellyfin.spec.ts`.
- An entry for the app in `validation.yaml` under `apps:` (auth strategy,
  validation level, file globs, `e2e-project`). `./bloud validate` infers
  the affected apps from this registry, so keeping it in sync means your
  app's tests run automatically whenever its files change.

## Reference Apps

The fastest way to grok the system: pick the app closest to yours and read it
top to bottom.

| App | Pattern |
|---|---|
| `apps/jellyfin` | LDAP SSO, setup wizard, plugin config, media libraries |
| `apps/sonarr` / `apps/radarr` / `apps/prowlarr` | Servarr family (same codebase): `config.xml` auth pre-seed (`AuthenticationMethod=External`) plus the shared `pkg/servarr` config/API helpers |
| `apps/qbittorrent` | forward-auth over an INI config the app itself also rewrites: managed-key merge, no blanket overwrite |
| `apps/seerr` | `sso.strategy: none` and a first-run onboarding driven from a sibling app's generated admin credentials |
| `apps/authentik` | Multi-container, LDAP infrastructure, API token management |
| `apps/immich` | Database integration, OIDC SSO |
| `apps/affine` | Own postgres+redis, OIDC config file, first-run owner bootstrap (see `INTEGRATION.md`) |
| `apps/navidrome` | Forward-auth SSO with bypass paths, simple single-container app |
| `apps/homeassistant` | native-oidc via a pinned custom component: `pkg/appasset` remote install + sha256 provenance, marker-based YAML config merge, `Deps.RestartContainer`-driven reload |
| `apps/paperless-ngx` | Own postgres+redis plus gotenberg/tika sidecars, dotenv config file generated for django-allauth OIDC, internal admin account |
| `apps/vaultwarden` | Single container, dotenv config file for built-in OIDC, extra OIDC scopes and token lifetime via `sso.scopes`/`sso.accessTokenMinutes`, `SSO_ONLY`, a container command wrapper behind an opt-in dev switch |

Stuck or unsure? The best place to start is `INTEGRATION.md` in whichever
reference app shares your SSO strategy; each one documents its own
provider quirks and verification steps. And welcome aboard: every app merged
here makes the whole system more useful.
