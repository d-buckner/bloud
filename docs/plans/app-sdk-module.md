# Plan: Extract the app-SDK module, invert `Deps.HTTP`/`Deps.Assets`, then split `bindings`

> Status: draft (2026-10-15). Not started. Measured against `main` at `5d3ed7d`.
> Parent: [configurator-sdk-surface.md](configurator-sdk-surface.md) Step 4.
> Resolves parent open question 2 (module location). Implements issue #332.
> Debt ledger: [tech-debt.md](../operations/tech-debt.md).

## Why

Steps 1 and 2 of the parent plan shipped. `Deps` is now four leaf fields
plus three subsystem groups, and the binding credential fields use the one
`Secret` type. What remains is the parent plan's Step 4: make `apps/` depend
on a published SDK instead of the whole `services/host-agent` module, then
split the author contract from the contract vocabulary. This is the change
that makes the app directory a real module boundary rather than a directory
convention (review finding 3.5).

## Resolved decision: module location

The SDK becomes a top-level `pkg/` module with the import path
`codeberg.org/d-buckner/bloud/pkg`.

The choice follows invariant 14, which already says the packages under
`services/host-agent/pkg/` are "candidates for extraction to a shared
`pkg/` module". Invariant 13 rules out `services/app-sdk/`: that directory
is for code shipped to a machine where host-agent does not run, and the SDK
is not that. A top-level `pkg/` also reuses the name the repo already gives
these packages.

## Scope correction: move nine packages, not seven

The parent plan and review finding 3.5 list seven packages to move. A grep
against `main` shows `apps/` also imports two more from
`services/host-agent/pkg/`:

- `authentik` (affine, authentik, radicale)
- `bootstrap` (immich, navidrome, paperless-ngx)

Both are author-facing. `authentik` imports only `appclient` and
`configurator`. `bootstrap` imports only `configurator`. If they stay in
host-agent, `apps/` keeps a `require` on the runtime module and the boundary
stays broken.

So the move is all nine packages, which is everything under
`services/host-agent/pkg/`. The directory disappears. `slug` is host-only
today (the catalog loader imports it) but is a leaf, so it moves too and
keeps the module whole.

## Final module layout

```
pkg/                          module codeberg.org/d-buckner/bloud/pkg
  go.mod
  configurator/
    interface.go              NodeLifecycle, AppState, PreStartResult,
                              PhaseBudget, SessionRevoker, Remover,
                              PortReporter, Configurator, LDAPOutput,
                              OIDCOutput, AppSecretsProvider
    factory.go                Deps + subsystems, Factory, ExecFunc,
                              ContainerRunningFunc, RegisterFactory,
                              MustRegisterFactory
    deps.go                   HTTPFactory, AssetInstaller interfaces
    registry.go
    ini.go
    postgres.go               imports github.com/jackc/pgx/v5
    templatevars.go
    urls.go
    interfaces.go             RegistryInterface
  bindings/                   new package, from move 3
    bindings.go               ProviderRef, ProviderKind, Secret, the twelve
                              binding types, Integrations
  appclient/                  + factory.go (Factory type)
  appasset/                   Installer satisfies configurator.AssetInstaller
  managedfile/
  servarr/
  slug/
  xmlutil/                    imports github.com/beevik/etree
  authenticak/
  bootstrap/
```

The SDK's third-party dependencies are small and verified by grep:
`github.com/jackc/pgx/v5` (configurator/postgres.go),
`github.com/beevik/etree` (xmlutil), and `github.com/stretchr/testify`
for tests. Nothing under the nine packages imports an `internal/` package.

## The three moves

The issue forbids three separate cosmetic PRs. This is one PR. Inside it,
the work runs in this order, and each move compiles and tests green before
the next starts.

### Move 1: extract the module

1. Create `pkg/go.mod` with `module codeberg.org/d-buckner/bloud/pkg`,
   `go 1.25.0`, and the dependencies above.
2. `git mv` the nine packages from `services/host-agent/pkg/` to `pkg/`.
3. Rewrite the import paths. In `apps/`, `services/host-agent/`, and the
   moved packages' own cross-imports, change
   `codeberg.org/d-buckner/bloud/services/host-agent/pkg/X` to
   `codeberg.org/d-buckner/bloud/pkg/X`.
4. Edit `apps/go.mod`: delete the `require
   codeberg.org/d-buckner/bloud/services/host-agent` line and its
   `replace`, add `require codeberg.org/d-buckner/bloud/pkg v0.0.0` and
   `replace codeberg.org/d-buckner/bloud/pkg => ../pkg`.
5. Edit `services/host-agent/go.mod`: add `require
   codeberg.org/d-buckner/bloud/pkg v0.0.0` and
   `replace codeberg.org/d-buckner/bloud/pkg => ../../pkg`. Keep the
   existing `apps` require and replace.
6. Run `go mod tidy` in all three modules. `apps/go.mod` sheds the
   host-agent transitive indirect block (pgx, pgx deps, and friends).

At the end of move 1, `apps/` has no import of
`codeberg.org/d-buckner/bloud/services/host-agent`.

### Move 2: invert `Deps.HTTP` and `Deps.Assets`

Today the dependency runs the wrong way. `configurator` owns a concrete
`ClientFactory` struct that builds `appclient.Client` values, and `Deps`
holds concrete `appasset.Installer`. The SDK must declare the interfaces,
and the host must inject the implementations.

1. Move `ClientFactory` out of `configurator/clientfactory.go` into
   `appclient/factory.go`, renamed `appclient.Factory`. The fields
   (`Transport`, `Retry`, `Logger`) and the method `New(spec Spec) *Client`
   keep their meaning. Move its test too.
2. Delete `configurator/clientfactory.go`. In its place, add
   `configurator/deps.go`:

   ```go
   type HTTPFactory interface {
       New(spec appclient.Spec) *appclient.Client
   }

   type AssetInstaller interface {
       Install(ctx context.Context, a appasset.Asset) (bool, error)
   }
   ```

3. Change the `Deps` field types: `HTTP HTTPFactory`, `Assets
   AssetInstaller`.
4. Change every parameter and helper that today takes
   `configurator.ClientFactory` to take `configurator.HTTPFactory`. That is
   `servarr.NewClient`, `authentik`'s client factory parameter, and each
   app's `newAPI(...)` helper. The helpers still call `.New(spec)`, so
   their bodies do not change.
5. Rewrite the test literals. `configurator.ClientFactory{...}` becomes
   `appclient.Factory{...}`, and `HTTP: configurator.ClientFactory{}`
   becomes `HTTP: appclient.Factory{}`.
6. In `internal/appconfig/register.go`, inject `appclient.Factory{...}`
   for `HTTP` and keep `appasset.Installer{...}` for `Assets`.

`configurator` still imports `appclient` and `appasset` for the types the
two interfaces name. That is now a within-module import, so the SDK stays
self-contained. The inversion is that `Deps` holds interfaces, not
concrete structs, and the host picks the implementations.

### Move 3: split `bindings`

1. Create `pkg/bindings/bindings.go`. Move into it, from
   `configurator/interface.go` and `configurator/secret.go`: `ProviderRef`,
   `ProviderKind` and its constants, `Secret` and its methods, the twelve
   binding types, and `Integrations`.
2. Change `configurator.AppState.Integrations` to `bindings.Integrations`.
   `configurator` imports `bindings`.
3. Rewrite the consumers. `configurator.XBinding` becomes
   `bindings.XBinding`, `configurator.Secret` becomes `bindings.Secret`,
   `configurator.ProviderRef` and `configurator.ProviderKind` become
   `bindings.ProviderRef` and `bindings.ProviderKind`. The churn is about
   17 non-test files across ten app packages, plus `servarr` and the
   orchestrator resolver, and about 20 test files. A grep counts the files
   that name each binding type.

After move 3, `configurator` holds the author contract and the host
plumbing. `bindings` holds the contract vocabulary a consumer reads. An
app that only writes files imports `configurator` alone. A consumer of a
contract imports both.

## What NOT to do

- No generic consumer bindings. Keep the twelve payload types typed. The
  binder table (parent plan Step 3, a separate issue) is the data-driven
  half, not this.
- Do not move `authentik` or `bootstrap` into `internal/`. They are
  author-facing and belong in the SDK.
- Do not reorder the three moves. The bindings split is the last step,
  after the module exists and the interfaces are inverted. That ordering is
  what makes the split a boundary and not a rename.
- Do not touch the registration mechanism. `MustRegisterFactory`, blank
  imports, and `NodeNames()` stay as they are.

## Sequencing

One PR. Three commits inside it, one per move, each green:

| Commit | Change | Gate |
|---|---|---|
| 1 | Extract the `pkg/` module, rewrite imports, edit both go.mod files | `go test ./...` in `pkg`, `apps`, `services/host-agent` |
| 2 | Invert `Deps.HTTP` and `Deps.Assets` | same, plus the conformance harness |
| 3 | Split `bindings` | same, plus the orchestrator resolver tests |

## Verification

- `cd pkg && go test ./...`
- `cd apps && go test ./...`
- `cd apps && go test ./... -run TestConformance`
- `cd services/host-agent && go test ./...`
- `go test ./internal/engine/orchestrator/...` after move 3.
- `./bloud validate --tier fast` for prose, em dash, doc links, image pins,
  file length, gofmt, golangci-lint, and license headers.
- Confirm `apps/go.mod` no longer requires
  `codeberg.org/d-buckner/bloud/services/host-agent`.
- Confirm `grep -r "services/host-agent/pkg" apps/ services/` returns
  nothing after move 1.

## How to know it worked

| Metric | Before | Target |
|---|---|---|
| `apps/` dependency | whole `services/host-agent` module | `pkg` module only |
| `interface.go` line count | 576 at plan time | under 350 |
| `Deps.HTTP` and `Deps.Assets` | concrete structs owned by `configurator` | interfaces declared in the SDK |
| `services/host-agent/pkg/` | nine packages | gone |
| `configurator` imports of `appclient`/`appasset` | wrong direction (host-owned concrete types) | within-module interface types |

## Risk

The churn is large and mechanical. The mitigation is the order: each move
is a compile-checked step, so a broken intermediate state fails the build
and never reaches review. The one real unknown is what `go mod tidy` drops
from `apps/go.mod`. Run it in move 1 and inspect the diff before move 2.
