# Plan: Remove the app-addition tax

> Status: landed 2026-10-14. Archived 2026-10-15. All six changes shipped: A
> derives the validation app registry from the catalog (#324), B derives the
> debug-port list from the catalog (#338), C makes a forgotten contract binding a
> build failure (#325), D takes app dev switches out of CI and the CLI and puts
> them in metadata (#326), E shrinks the per-app specs to the generic rungs the
> framework registers (#339), and F drops the conformance table's `DefaultPort`
> column in favour of a configurator that reports its own port (#334).
> Written 2026-10-12 against `main` at `e66f2f6`.
> Companion to [review-2026-10-03-aposd.md](../../specs/review-2026-10-03-aposd.md),
> which reviews the runtime's module shapes. This plan covers a different axis: the
> *workflow cost of adding a catalog app*, which that review read only for context.
> Debt ledger: [tech-debt.md](../../operations/tech-debt.md).

## Why

Adding an app costs far more files than the app itself. Measured over the 16 app-add
commits found by `git log --diff-filter=A -- apps/*/metadata.yaml` (`affine`, 2026-08-23,
through `ha-mcp`, 2026-10-12):

| Measure | Value |
|---|---|
| File-touches in those commits | 489 |
| File-touches outside `apps/<name>/` | 258 (53%) |
| Added lines | 44,001 |
| Added lines outside `apps/<name>/` | 15,097 (34%) |

The line split and the file split disagree, and that disagreement is the finding. The
outside edits are small and numerous: 15 lines total to repeat a port number, 12 lines
total across CI workflows, 172 lines to re-declare apps in `validation.yaml`. One
decision ("this app exists") forces edits in six to ten unrelated modules. APoSD names
that symptom change amplification, and names its cause: the same fact is represented in
several places that must then change together. All 16 commits touched `validation.yaml`.
Only 3 touched a port map, and that is not the map being cheap: it is the map being
forgettable, which is what section 2 shows it drifted into.

Breakdown of where the file-touches landed:

| Where | File-touches | +lines | Character of the edit |
|---|---|---|---|
| `apps/<name>/` | 231 | 28,904 | the app |
| other host-agent/CLI source | 73 | 3,161 | mostly a real new capability, see "Not a tax" |
| Go unit tests | 44 | 3,789 | real |
| docs (`AGENTS.md`, README, guides, features) | 46 | 963 | tax, partly generatable |
| `e2e/` | 26 | 1,997 | mostly real, partly copy-paste |
| `validation.yaml` | 16 | 172 | tax, fully derivable |
| contract registry (three files) | 15 | 301 | tax, partly derivable |
| VM port-forward map (`cli/backend/*`, `dev/*.yaml`) | 14 | 15 | tax, fully derivable |
| `docs/plans/*.md` | 13 | 2,812 | the design doc for the feature, real work |
| host-agent integration tests | 6 | 1,875 | real |
| `.github/workflows/` | 5 | 12 | tax, app knowledge in CI |

The rows marked tax (validation registry, port map, contract registry, CI, and part of
docs and e2e) are roughly 100 of the 258 outside touches. Those are what this plan
removes.

## What the tax is, per edit site

### 1. `validation.yaml`'s `apps:` block is fully derivable, and partly dead

Diffing all 20 entries against `apps/*/metadata.yaml` and the filesystem:

| Field | Derivation | Agreement |
|---|---|---|
| the app set | every `apps/*/metadata.yaml` without `isSystem: true` | 20/20 (`authentik` and `traefik` are the two system apps and are absent) |
| `auth` | `sso.strategy`, defaulting to `none` | 20/20 |
| `files` | `apps/<name>/**` | 20/20 |
| `e2e-project` | the app name, exactly when `e2e/tests/<name>.spec.ts` exists | 20/20 |
| `validation-level` | nothing reads it | dead: parsed into `cli/inference.go` and never used |

`AGENTS.md` lists keeping this block in sync as open debt, and
[contributing-apps.md](../../guides/contributing-apps.md) makes it a required step. That is
a manual sync standing where a derivation belongs.

The one honest cost: the block carries 34 lines of *reasoning* (why Radicale is
`ldap`, why Calino is `forward-auth`, why the MCP wrappers have no browser journey).
That prose is worth keeping. It belongs next to the app it explains, in
`apps/<name>/INTEGRATION.md`, not in a registry two directories away.

### 2. One port number, eight copies

`apps/vaultwarden/metadata.yaml` says 8222. So do `cli/backend/lima.go`,
`cli/backend/qemu.go`, `cli/backend/native.go`, `dev/lima.yaml`, `dev/qemu.yaml`,
`AGENTS.md`'s port table, and `apps/conformance_test.go`.

Two findings make this worse than duplication:

- **Most of the Go copies are dead.** `Host().Ports()` is read at three call sites
  (`cli/dev_deploy.go`, `cli/dev_run.go`, `cli/dev_ready.go`) and only ever asks for
  `host-agent` and `traefik`. The eight app entries are never read by anything. They are
  documentation written in Go.
- **The live copy has already drifted, in both directions.** `qemu.go`'s `launchArgs`
  builds the actual `hostfwd` list from a fourth literal list, and that list omits guest
  8000 (paperless-ngx) and 8222 (vaultwarden). Meanwhile `QEMUBackend.Host().Ports()`
  still advertises both. On the QEMU backend those two debug ports are unreachable, and
  the map that claims them is the reason nobody noticed. The opposite half is quieter: the
  four MCP wrappers added since (`ha-mcp` 8086, `arr-mcp` 6060, `dav-mcp` 9333,
  `affine-mcp` 9222) were never added to any forward list, so their documented debug
  ports do not exist on any backend.

This is the strongest argument in the plan: the duplication did not merely cost edit
time. Each copy is internally consistent, so no check can catch a copy that is wrong or a
copy that was never updated, and the result is a system whose own documentation about its
ports is false in both directions.

### 3. A new integration contract is a three-file edit, written down as an invariant

Invariant 15 states that a new capability is "a contract entry, a payload type in
`pkg/configurator`, and one arm in `bindContract`". The repo has codified its own change
amplification. The `requestManager` contract (`arr-mcp`) is the specimen: 7 lines in
`internal/catalog/contracts.go`, 17 in `pkg/configurator/interface.go` (a struct plus a
field on the shared `Integrations`), 40 in `orchestrator/integrations.go`.

The arms are shape-identical. `Contract` already declares `Secrets` and `Values`, and
`bindContract` reads exactly those keys through `publishedSecret` and `contractValue`.
`pvr` is `{secret}`, `sso` is `{secret}`, `icsFeed` is `{secret, path, calendarName}`,
`mcp` is `{secret, serverName, path}`. The switch restates the registry in Go. And
because the payload type lands in `pkg/configurator`, every new contract widens the
interface all 20 apps compile against.

### 4. CI and the CLI know facts about individual apps

`.github/workflows/e2e-apps.yml` contains
`BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP: ${{ matrix.app == 'vaultwarden' && '1' }}`, and
`cli/devenv.go` hardcodes the same variable name. `cli/e2e_app.go` enumerates the app
list twice in help and error strings, which is why the Vaultwarden and Paperless-ngx
commits each edited it to add a name to a sentence.

### 5. Four e2e specs differ only in a label

`radarr.spec.ts`, `sonarr.spec.ts`, `prowlarr.spec.ts`, `qbittorrent.spec.ts` are 49-50
lines each, and `radarr` against `sonarr` differs in 9 lines, every one of them the app
name or label. `describeApp` in `e2e/lib/app-suite.ts` already abstracts the harness and
already registers the two forward-auth rungs from an options object. What remains
copy-pasted is the three generic rungs (converges, in catalog, converged tile), which are
identical for every app.

### 6. The conformance table re-declares the port a third time

`apps/conformance_test.go` carries `DefaultPort` per row, and it equals
`metadata.yaml`'s `port` in 22/22 rows. The harness's own `port_matches_metadata` check
exists precisely to assert that equality, so the table is a third copy of a fact the
harness is built to compare against.

## The changes

Six independent changes, each shippable alone. Ordered by (drift risk removed) x (cost).

### A. Derive the `apps:` block; delete `validation-level`

**Derivation rules** (the table in section 1, verbatim):

| Derived field | Rule |
|---|---|
| app set | `apps/*/metadata.yaml`, excluding `isSystem: true` |
| `auth` | `sso.strategy`, or `none` when absent |
| `files` | `apps/<dir>/**` |
| `e2e-project` | `<dir>` if `e2e/tests/<dir>.spec.ts` exists, else unset |

**Design choice: derive at runtime, do not generate a block.** The repo already has two
generated-artifact pipelines (`depgraph --write/--check`, `catalogdoc --write/--check`),
and a third would work. It is the weaker option here. A generated block still needs a
regeneration step, still leaves a stale committed file as a possible state, and still
costs a CI gate. `validation.yaml`'s `apps:` block has no reader that needs it to be a
file: `loadManifest(root)` already receives the repo root, and three functions consume
the map, all of them path-matching or set-listing (`detectAffectedApps` in
`cli/inference.go`; `e2eProjects` and `appForFile` in `cli/e2e_affected.go`). Replace the
parsed map with a `deriveApps(root)` call beside it and the drift becomes
unrepresentable rather than merely checked.

`inference.paths` stays in `validation.yaml`. That block is about paths and command IDs,
which nothing else in the tree declares, so it is a source, not a copy.

**Delete `validation-level`**: the struct field in `cli/inference.go` and all 20
declarations. Dead data in a file whose purpose is to say what to run is a false
statement about the system.

**Move the reasoning.** The 34 comment lines explaining each app's strategy move to the
app's `INTEGRATION.md` (creating it where absent). The registry loses prose it should not
have owned; the app gains the explanation where a reader of that app will find it.

**Guards:**

- A test that the derived set, auth values, globs, and e2e projects equal the block being
  deleted (a golden snapshot taken before the change, so the derivation is pinned to the
  state the human wrote, not to the derivation's own output).
- A test that a fixture app directory with a `metadata.yaml` appears in the derived set
  with no `validation.yaml` edit. That is the regression this whole plan exists to
  prevent, asserted directly.
- Update `cli/inference_test.go`, which currently passes a synthetic manifest with an
  `Apps` map.

**Cost:** M. **Risk:** Low. Nothing at runtime changes; only which files `./bloud validate`
and `./bloud e2e affected` think are affected.

### B. Derive the debug-port list from the catalog

Three separate moves:

1. **Delete the eight unread app entries** from `Ports()` in all three backends. The
   `host-agent` and `traefik` keys stay: they are read, and they are not app facts. This
   removes 24 lines and the false claim.
2. **Derive the forward list from the catalog.** One function returns the debug ports:
   infra ports (ssh 22, host-agent 3000, Traefik 80 mapped to host 8080) plus every
   catalog app's published port. All are already in metadata: `port:` for the app's UI
   port, and `containers[].ports` for the authentik LDAP outpost's 3389. `qemu.go`'s
   `launchArgs` consumes it, which is what makes the drift in section 2 structurally
   impossible.
3. **Generate the `dev/*.yaml` port blocks and the `AGENTS.md` table** from the same
   function, using the `generatedBlock` machinery in `cli/genblock.go` and the
   `--write`/`--check` shape the other two generators use. Add the `--check` to the fast
   tier beside `depgraph-check` and `catalogdoc-check`.

**Note on scope:** deriving forwards every catalog app's declared port, which is what
fixes the four missing MCP wrappers. Consistency is the right default here: a declared
`port:` is a port the app serves, and a debug forward costs nothing. If a port should
genuinely never be forwarded, that wants an explicit metadata opt-out, not a line someone
forgot to add.

**Note for the PR:** port forwards are provisioning-time settings, so an existing dev VM
keeps its old list. `AGENTS.md` already documents the recreate requirement for the same
reason; the PR should repeat it in the changelog line.

**Guards:** a test that the derived list equals the current `lima.yaml` list (golden,
again pinned to the human-written state); a test that `launchArgs` forwards every
derived port, which fails today and is the bug fix; the `--check` gate.

**Cost:** M. **Risk:** Low-Med. It changes what the backends forward, which is a behavior
change in the good direction (the ports that were advertised but never bound, plus the
four wrappers that were never listed, become reachable) and should be called out as such.

### C. Make a forgotten contract binding a build failure, then shrink the edit to two files

Two steps, deliberately not one.

**C1 (do now): a binder table plus a completeness test.** Replace `bindContract`'s switch
with a table keyed by contract name, each entry a closure that appends the right typed
binding to the right `Integrations` slice. The payload struct stays typed and named,
because `binding.Values["path"]` would trade change amplification for obscurity, which is
the symptom the 2026-10-03 review already flags elsewhere (stringly typed statuses,
finding 3.8). The gain is that the table's keys are data, so a test can compare them
against `catalog.Contracts()`: every contract that declares a payload has exactly one
binder, and every binder names a contract that declares a payload. That is the house
pattern, the same rule `scripts/pinned-images.mjs` uses for `EXCEPTIONS` and
`scripts/file-length.mjs` uses for `BASELINE`: an exemption that guards nothing is itself
a failure. Today the mismatch is only a runtime `WARN` in `warnUnboundPayload`, which is
the weakest possible signal for "a consumer is silently receiving nothing".

Edit sites go from three to two (registry entry, payload struct plus its binder, which
can sit in the same file), and the third becomes impossible to forget rather than merely
warned about.

**C2 (record as direction, do not do here): the registry as catalog data.** The real fix
is that contract vocabulary is app-domain knowledge sitting in host-agent's `internal/`,
so declaring new vocabulary requires editing the runtime. Moving it into the catalog
(a `contracts:` document loaded with the apps) would make it data. It is out of scope
because it forces generic payloads (the obscurity cost above) and because the dependency
runs the wrong way: `apps/` imports `pkg/configurator`, so the registry cannot live in
`apps/` without inverting that. If it is taken up, it wants its own plan.

**Guards:** the completeness test; the existing shipped-catalog contract-declaration test
stays as the metadata-side half.

**Cost:** S for C1, L for C2. **Risk:** Low for C1 (behavior-preserving), Med for C2.

### D. Take app-specific env out of CI and the CLI

Add to `metadata.yaml` a declaration of what an e2e or dev run needs:

```yaml
e2e:
  env:
    BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP: "1"
```

`cli/e2e_app.go` and the lifecycle runner read it from the catalog and set it, so
`.github/workflows/e2e-apps.yml` loses its `matrix.app == 'vaultwarden'` special case and
stops knowing anything about any app. `cli/devenv.go` keeps the two deployment-wide
entries (`BLOUD_TRUSTED_PROXY_NETS`, `BLOUD_PUBLIC_SCHEME`) and takes app-specific names
from the catalog instead of a literal. `cli/e2e_app.go`'s two hardcoded app enumerations
become a catalog read, or drop the enumeration and say "any catalog app with a spec".

**Guard:** a test that the env a run receives for a fixture app comes from metadata, and
that a metadata entry naming no known variable fails the catalog load rather than being
ignored. The loader rejecting a bad declaration is the existing house answer.

**Cost:** S. **Risk:** Low.

### E. Shrink the four forward-auth specs to their app-specific values

Move the three generic rungs (converges to running, appears in the catalog, appears as a
converged tile) into `describeApp`, which already owns the two forward-auth rungs and
already receives the label. Each of the four specs collapses from ~49 lines to the
options object: the label, the title pattern, the app-shell selector.

Keep one file per app. Playwright filters by `--grep` on the describe name
(`BLOUD_E2E_PLAYWRIGHT_FILTER`), the CI matrix keys on the app name, and change A derives
`e2e-project` from the spec file's existence. Generating spec files would satisfy all
three but adds a build step to the test tree for no gain over a 12-line file.

Apps with genuinely app-specific journeys (Jellyfin's wizard, Immich's onboarding,
Hermes, Paperless-ngx's django-allauth path) keep their bodies. The change is for the
shape where nothing varies but the name.

**Guard:** the specs still run under the same project names, asserted by
`./bloud e2e affected` output for an `apps/radarr/`-only change set, which already has a
test in `cli/e2e_affected_test.go`.

**Cost:** S. **Risk:** Low, but it touches the mandatory regression gate, so it lands
with a full e2e run, not on inference.

### F. Drop the conformance table's `DefaultPort` column

Read the port from `metadata.yaml` in the harness instead. The `port_matches_metadata`
check then compares the constructor's default against the catalog rather than against a
third transcription of the catalog, which is what it was always meant to do.

**Correction to the first draft of this plan.** An earlier version proposed deriving
`NodeNames()` from the configurator registry to remove a double entry. That was wrong.
`TestNodeNamesStable` asserts the exact node list on purpose, so that a rename has to be
a deliberate edit rather than a silent consequence of a refactor. The duplication is a
stability pin, not an accident, and it stays. The `WithSSO`, `WithLDAP`, `WithAgent`, and
`WithAppAPI` flags also stay: they are inputs about the configurator's code paths, and
they only mostly track `sso.strategy` (Radicale is `ldap` and needs both `WithSSO` and
`WithLDAP`), so deriving them would create exceptions worse than the table.

**Cost:** S. **Risk:** Low.

## Not a tax (leave it alone)

The 73 "other source" touches are mostly the process working, not failing. AFFiNE built
`authentik.UserEmailDomain` and the verified-email scope mapping, Vaultwarden built the
OIDC tuning, Hermes built the loopback issuer. The first app that needs a capability
builds it, and `pkg/appclient`, `pkg/managedfile`, and `pkg/appasset` show those
extractions landing as genuinely deep modules. That is strategic programming in APoSD's
sense, and this plan must not be read as a claim that app-add PRs should touch only
`apps/`.

Also out: `docs/plans/*.md` per feature (the design doc is the work), the integration
tests, and the conformance harness's behavioral flags.

One known item this plan neither causes nor fixes: `pkg/servarr` and `pkg/authentik` hold
per-app client code in host-agent's shared `pkg/`, against the rule written in
[contributing-apps.md](../../guides/contributing-apps.md). That doc already records them as
predating the rule and under opportunistic migration.

## Sequencing

Each PR is behavior-preserving except B, which fixes a broken port forward.

| PR | Change | Depends on |
|---|---|---|
| 1 | A: derive `apps:`, delete `validation-level`, move the prose to `INTEGRATION.md` | nothing |
| 2 | B: delete dead `Ports()` entries, derive the forward list, generate the `dev/*.yaml` and `AGENTS.md` blocks | nothing |
| 3 | C1: binder table plus completeness test | nothing |
| 4 | D: `e2e.env` in metadata, CI special case deleted | nothing |
| 5 | E: generic rungs into `describeApp`, four specs shrink | nothing |
| 6 | F: conformance `DefaultPort` dropped | nothing |

PR 1 and PR 2 are the recommended first pair: both are pure derivations of facts that
already have a single source, both reuse code that exists (`loadAppMetadata` and
`loadCatalog` in the CLI, `generatedBlock` in `genblock.go`), both delete a documented
"keep in sync" obligation, and neither touches the orchestrator.

Docs to update in the same changes: `docs/README.md`'s index row for this plan, the
required-steps list in `guides/contributing-apps.md` (the `validation.yaml` step goes
away, the port step goes away), and `AGENTS.md`'s known-debt line about keeping
`validation.yaml` in sync, plus its port table if it becomes generated.

## How to know it worked

Re-measure with the same method, on the next three app additions:

| Metric | Before | Target |
|---|---|---|
| file-touches outside `apps/<name>/` | 53% | under 35% |
| recurring edit sites per app add (`validation.yaml`, port map, CI, contract registry) | 4 | 0 for a plain app, 2 for a new contract |
| copies of an app's port number | 8 | 1 in metadata, plus generated blocks |
| hand-maintained registries an app must be added to | 7 (`validation.yaml`, the three backend port maps, `apps/registry.go`'s blank import and its `NodeNames()` pin, the conformance table) | 3 (the blank import, the `NodeNames()` pin, the conformance table) |

If a new app still requires a hand edit outside `apps/` for a fact it already declared in
`metadata.yaml`, the plan is incomplete and the missing derivation should be named here
rather than absorbed.

## Decisions

| Question | Decision | Why |
|---|---|---|
| Derive at runtime or generate a committed block? | Runtime for `validation.yaml`, generated blocks for `dev/*.yaml` and `AGENTS.md` | The first has no reader that needs bytes on disk; the second are read by `limactl` and by humans, so they must exist as files |
| Keep typed contract payloads or go generic? | Typed | `Values["path"]` replaces change amplification with obscurity, the wrong trade |
| Keep `validation-level`? | Delete | Parsed by nothing; see change A |
| Keep `NodeNames()` duplication? | Keep | `TestNodeNamesStable` is a rename guard, not an oversight |
| Generate the e2e specs? | No | `--grep`, the CI matrix, and change A's derivation all key on the per-app file; a 12-line file is cheap |

## Open questions

1. Does the derived app set need an opt-out? Today every non-system app is in the
   registry. If an app ever needs to be excluded from validation inference, that needs a
   metadata field, not a hand-edited registry entry. Not built until a real case exists.
2. Should `e2e.env` be one field or two (`dev.env` and `e2e.env`)? The CLI passthrough
   and the CI run want the same map today. One field until they diverge.
3. Change B makes the backends forward ports they never bound (8000 and 8222 on QEMU, and
   8086, 6060, 9333, and 9222 everywhere). Worth confirming no existing guest service
   expects those host ports to be free.
