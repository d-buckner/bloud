# Plan: A smaller, clearer app-SDK surface (`pkg/configurator`)

> Status: draft (2026-10-12). Not started. Measured against `main` at `e66f2f6`.
> Companion to [review-2026-10-12-aposd-appsurface.md](../specs/review-2026-10-12-aposd-appsurface.md)
> (findings 3.1, 3.2, 3.5, 3.6) and to
> [app-add-tax.md](app-add-tax.md) (whose change C is the binder half of this plan).
> Debt ledger: [tech-debt.md](../operations/tech-debt.md).

## Why

`pkg/configurator` is the interface between the runtime and every app that will
ever be written, and it is wide in proportion to the host's capabilities rather
than to any one app's needs. One 576-line `interface.go` bundles three things:

1. the **author contract** an app implements (`NodeLifecycle`, `AppState`,
   `PreStartResult`, `PhaseBudget`, `SessionRevoker`, `Remover`);
2. the **host plumbing** the runtime injects (`Deps`, `AppSecretsProvider`,
   `Registry`, `Factory`, `ExecFunc`, `ContainerRunningFunc`);
3. the **contract vocabulary** a consumer reads (`ProviderRef`, `ProviderKind`,
   twelve binding types, `Integrations`).

APoSD names the symptom: the package is shallow relative to its job, because it
exposes almost as much as it hides. The goal of this plan is to shrink and
sharpen that surface without regressing the parts that already work (the
conformance harness, the loader's metadata validation, `pkg/servarr`).

## A correction to the review's first assessment

Finding 3.1 proposed moving the twelve binding types into a sibling `bindings`
package. A grep against `main` changes the cost of that move and therefore its
place in the sequence:

- ~17 non-test files across 10 app packages **name** a binding type directly
  (the dominant pattern is a typed helper like
  `func inferenceBinding(state *configurator.AppState) (configurator.InferenceBinding, bool)`,
  not field iteration), plus `pkg/servarr` and the orchestrator's resolver;
- ~20 test files do the same.

So the split is roughly 40 files of mechanical churn (`configurator.XBinding` →
`bindings.XBinding` plus an import), and its author-facing benefit is smaller
than first claimed, because real configurators name the types anyway. The split
is **not** wrong; it is the right shape for the eventual SDK module (step 4).
But it is a *destination*, not a first step, and it must not be done in
isolation. The first three steps below are the high-value, low-churn work; they
stand on their own and are prerequisites for the split.

## Current shape (inventory)

| File | Lines | Holds |
|---|---|---|
| `interface.go` | 576 | `NodeLifecycle`, `AppState`, `PreStartResult`, `PhaseBudget`, `SessionRevoker`, `Remover`, `AppSecretsProvider`, `LDAPOutput`, `OIDCOutput`, `ProviderKind`, `ProviderRef`, 12 binding types, `Integrations` |
| `factory.go` | 153 | `Deps` (11 fields), `Factory`, `ExecFunc`, `ContainerRunningFunc`, `RegisterFactory`, `MustRegisterFactory` |
| `registry.go` | 124 | `Registry` (lazy factory instantiation) |
| `ini.go` | 161 | `INIFile` / `INISection` config editing |
| `templatevars.go` | 96 | `TemplateVars` (the LDAP outpost token store) |
| `postgres.go` | 63 | `EnsureDatabase` |
| `clientfactory.go` | 44 | `ClientFactory` (wraps `appclient`) |
| `urls.go` | 40 | `AppExternalURL` |
| `interfaces.go` | 25 | `RegistryInterface` |

`Deps` today: `Logger`, `Secrets`, `OperatorEmail`, `OperatorUsername`,
`PrimaryBaseURL`, `TraefikPort`, `RestartContainer`, `Exec`,
`ContainerRunning`, `HTTP`, `Assets`.

## Step 1: group `Deps` into subsystem values

The 11 fields cluster into four leaf fields and three subsystems. The leaf
fields stay top-level; the rest move into small structs:

```go
type Deps struct {
    Logger   *slog.Logger
    Secrets  AppSecretsProvider
    HTTP     ClientFactory
    Assets   appasset.Installer

    Identity IdentityDeps // OperatorEmail, OperatorUsername
    Address  AddressDeps  // PrimaryBaseURL, TraefikPort
    Runtime  RuntimeDeps  // RestartContainer, Exec, ContainerRunning
}
```

Each subsystem struct documents its own nil contract once ("nil in CLI/tests")
instead of the current per-field notes, and a factory *declares* what it uses by
which sub-values it reads.

**What changes:** every factory that reads a moved field (the seven fields in
`Identity`/`Address`/`Runtime`) changes one accessor (`deps.RestartContainer` →
`deps.Runtime.RestartContainer`). `deps.Logger`, `deps.Secrets`, `deps.HTTP`,
`deps.Assets` do not move. The conformance harness's `OfflineDeps` and the
orchestrator's `wire` construction are the other construction sites.

**Cost:** S. **Risk:** Low (mechanical, compile-checked). **Verification:**
`cd services/host-agent && go test ./...`, `cd apps && go test ./...`; the
conformance harness (`AssertNilDepsSafe`) already asserts a zero `Deps` is safe,
so a field moved without a nil guard fails there.

## Step 2: one `Secret` type for "empty means not-ready"

Twelve binding types each re-state the same rule in prose: an empty
`APIKey`/`Token`/`Password` means "the provider has not published it yet", never
"an empty credential". The rule is load-bearing (it is how a consumer tells
"not ready" from "nothing to give"), and it is written twelve ways.

Introduce a small named type and a helper, and use it in the binding fields:

```go
// Secret is a credential a provider publishes. Empty means "not published yet",
// never "an empty credential"; a consumer must treat it as not-ready and write
// nothing. The rule is stated once here instead of per binding field.
type Secret string

func (s Secret) Published() bool { return s != "" }
```

The twelve `string` credential fields (`PVRBinding.APIKey`,
`RequestManagerBinding.Password`, `MediaServerBinding.AdminPassword`,
`SSOBinding.APIToken`, `MCPBinding.Token`, `AppAPIBinding.Password`,
`AgentAPIBinding.APIKey`, `ICSFeedBinding.APIKey`, `InferenceBinding.APIKey`,
and the `appApi`/`clientPassword` halves) become `Secret`. Non-credential
fields (addresses, paths, names) stay `string`.

**Trade-off, stated:** this is the one step with a real consumer churn. Every
configurator that hands a credential to a string-typed API
(`req.Header.Set("X-Api-Key", b.APIKey)`) needs a `string(...)` conversion, or
the `Secret` type needs an explicit `String() string`. If that churn reads as
too much for the benefit, the fallback is the minimal version: keep `string`
fields and move the rule to a single package-level doc comment that all twelve
fields point at. Either version is fine; the point is one source for the rule,
not twelve paraphrases.

**Cost:** S (with conversions) or XS (doc-only fallback). **Risk:** Low.
**Verification:** `go test ./...` in both modules; the conformance harness
already fails a configurator that treats an empty binding as a real credential,
so the behavior is pinned.

## Step 3: a generic binder table + a completeness test (the `bindContract` switch)

This step lives mostly in the orchestrator and catalog, not in
`pkg/configurator`, but it is what completes the contract triangle the package's
vocabulary sits in. It is [app-add-tax.md](app-add-tax.md) change C1, restated:

- Replace `bindContract`'s switch (`internal/engine/orchestrator/integrations.go`)
  with a table keyed by contract name, each entry a closure that appends the
  right typed binding. The payload structs stay typed and stay in
  `pkg/configurator`; only the *binder* becomes data-driven over the contract's
  declared `Secrets`/`Values`.
- Add the house-pattern completeness test: every payload-bearing contract in
  `catalog.Contracts()` has exactly one binder, and every binder names a
  declared contract. Today a forgotten binder is only a `WARN` from
  `warnUnboundPayload`, the weakest signal in a codebase whose habit is turning
  warnings into build failures.

This does not itself shrink `pkg/configurator`; it removes the *reason* the
vocabulary keeps growing there by hand (each new contract currently demands a
new arm in the runtime).

**Cost:** S. **Risk:** Low (behavior-preserving). **Verification:** the
completeness test plus the existing `contract_declarations_test.go`
(metadata-side) and the shipped-catalog integration tests.

## Step 4 (destination, its own plan): extract an app-SDK module, and only then split

The end state that makes the surface *stay* small is a separate Go module for
the author-facing surface, so `apps/` depends on a published SDK rather than on
the whole `services/host-agent` module (finding 3.5). The package split from
finding 3.1 belongs here, not before:

1. Move `pkg/configurator`, and the packages it imports for its public types
   (`appclient`, `appasset`, `managedfile`, `servarr`, `slug`, `xmlutil`), into
   an `app-sdk` (or `pkg/`-rooted) module.
2. Invert the `Deps.HTTP` and `Deps.Assets` dependency: the SDK declares
   `HTTPFactory` and `AssetInstaller` interfaces, the host injects the
   `appclient`/`appasset` implementations. Today the direction is the wrong way
   (`configurator` imports `appclient` and `appasset`, so the SDK cannot be
   self-contained).
3. Only once the SDK exists, split `configurator` into the framework package and
   a `bindings` package, and accept the ~40-file `configurator.XBinding` →
   `bindings.XBinding` churn counted above. At that point the churn is the
   *definition* of the new boundary, not a cosmetic rename.

**Cost:** L. **Risk:** Med. This wants its own plan with the module layout, the
`replace` directive removal in `apps/go.mod`, and the interface-inversion
written out before any code.

## What NOT to do

- **No one-file-per-type split.** It produces shallow modules and churn with no
  information hiding.
- **No generic bindings on the consumer side.** `type Binding struct { Values
  map[string]string }` would collapse the twelve types to one but buys
  obscurity for the price of compile-time field names; that is the stringly typed
  disease the repo already fought (review finding 3.8). Keep the consumer-facing
  payloads typed. Only the *binder* (step 3) becomes data-driven.
- **Leave the registration mechanism alone.** `MustRegisterFactory` + blank
  imports + `NodeNames()` is sound, and the `NodeNames()` duplication is a
  deliberate rename guard (`TestNodeNamesStable`), not an oversight.
- **Do not do the `bindings` package split before steps 1-3 and the SDK module.**
  It is ~40 files of churn that only pays for itself as the boundary of the SDK.

## Sequencing

| PR | Change | Depends on |
|---|---|---|
| 1 | Step 1: group `Deps` | nothing |
| 2 | Step 2: `Secret` type (or the doc-only fallback) | nothing |
| 3 | Step 3: binder table + completeness test (= app-add-tax C1) | nothing |
| 4 | Step 4: SDK module extraction + interface inversion + `bindings` split | 1, 2 (its own plan) |

PRs 1-3 are independent and each shippable alone. PR 4 is the destination and
must not be attempted as three separate cosmetic moves.

## Verification

The canaries for this whole plan, in order of cheapness:

- `cd services/host-agent && go test ./...` and `cd apps && go test ./...` after
  every step (the two modules are the compile boundary).
- `cd apps && go test ./... -run TestConformance` exercises every configurator
  against the harness, so any `Deps` or `Secret` change that breaks the nil-safe
  or empty-means-not-ready contract fails there.
- `go test ./internal/engine/orchestrator/...` after step 3 (the resolver and
  its integration tests pin the bindings).
- `npm run check:no-emdash` and `npm run check:docs-links` if any doc comment
  moves (this plan itself is already clean).

## How to know it worked

| Metric | Before | Target |
|---|---|---|
| `interface.go` line count | 576 | under 350 after step 4 (vocabulary moved out) |
| `Deps` field count | 11 flat | 4 leaf + 3 subsystem groups |
| Sources of the "empty means not-ready" rule | 12 paraphrases | 1 (`Secret` or one doc comment) |
| Forgotten contract binder | a `WARN` log line | a build failure |
| `apps/` dependency | whole `services/host-agent` module | a published app-SDK module |

If a new contract still requires a hand-written arm in the runtime (after step
3) or a hand-edited type in `interface.go` (after step 4), the plan is
incomplete; name the missing derivation here rather than absorbing it.

## Open questions

1. **Resolved** (#333, 2026-10-14): the typed `Secret` shipped. It carries a
   `String()` method, so a consumer hands a credential to a string-typed API
   with `s.String()`.
2. **Resolved** ([app-sdk-module.md](app-sdk-module.md)): the SDK is a
   top-level `pkg/` module (`codeberg.org/d-buckner/bloud/pkg`). It is not a
   `services/` directory, so invariant 13 does not apply.
