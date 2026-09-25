# Refactor Plan: One Configurator Contract, Thirteen Apps That Actually Obey It

Target: configurator-layer conformance debt C5, C6, C7, C10, C11, C14, C15, plus
the C3 regression and the Servarr duplication the ledger never recorded. Driver:
long-term maintainability. The catalog grew from the eight apps the 2026-09-20
audit counted to thirteen user apps, and every drift item arrived in the new apps
too.

## Problem Statement

The configurator contract is documented well and enforced nowhere. `NodeLifecycle`
says what a configurator may do in each phase, and thirteen apps ignore the parts
nobody can check.

The contract says `PreStart` returns `changed` meaning "mounted file contents
were modified". The orchestrator uses that one boolean to decide whether to
delete and re-create the container. So a single field carries two unrelated
claims, and apps read it their own way. Home Assistant returns
`rpChanged || changed || ok || staleForce`, where `staleForce` is set by a live
HTTP probe of the running instance and means "this container is broken, recreate
me" while nothing was written to disk. Jellyfin returns
`pluginInstalled || networkChanged`. Sonarr documents that "directory creation
alone must never report a change" and then returns only the `EnsureExternalAuth`
result, which is a third meaning again. Thirteen apps, five shapes.

`PreStart` is documented as config files and directories only. Home Assistant
runs a network probe inside it and can force a container recreate from there.

The bootstrap-account convention is copy-pasted with three naming schemes
(`bloud-admin`, `bloud-bootstrap-admin`, `bloud-admin@<app>.localhost`) and
four failure semantics. Paperless returns `("", nil)` when it cannot verify its
admin. Home Assistant fails hard. Jellyfin and Immich create-or-skip. The same
shape of code answers "what do I do when the account I need cannot be
established" four different ways, and nothing says which one is right.

Cross-file agreement is held together by comments. Paperless' `providerID`,
`callbackPath`, and `confFileName` each carry a doc comment asserting they equal
something in `metadata.yaml`. No test checks any of them. Vaultwarden already
proved the better pattern, loading its own `metadata.yaml` with `yaml.Unmarshal`
and asserting the constants match, but nobody generalised it. Paperless uses a
hand-rolled line splitter that cannot see nested keys at all.

Registration drifted further since the audit, in the good direction: prowlarr,
radarr, and sonarr use a `nodeName` constant, the other ten hardcode the string
literal. Home Assistant is still the only app passing a real port (`8123`) where
every other app passes `0` and defaults inside the constructor.

File modes are per-app folklore: `0600` for affine, the HA block, the authentik
token, and vaultwarden; `0644` for immich, paperless, hermes, and the authentik
blueprint. Each has a rationale comment and there is no policy a new app can
follow.

**C3 has regressed.** The ledger records that the six no-op `Remove` methods were
deleted in 2026-09-20 and `Remover` made optional, with the orchestrator
type-asserting before calling it. Seven apps have since added
`func (c *Configurator) Remove(_, _, _) error { return nil }`: hermes,
vaultwarden, prowlarr, qbittorrent, radarr, seerr, sonarr. The type assertion
means these are dead code that actively lies: each one advertises "this app owns
its teardown" and does nothing.

**The duplication the ledger never counted.** `apps/sonarr/configurator.go` and
`apps/radarr/configurator.go` are 88 percent identical: 48 differing lines out
of 399, and the differences are prose and five constants. Both define the same
twelve functions in the same order. Their test files differ by 236 of about 1,050
lines. A shared `pkg/servarr` already exists underneath them with the client,
config, and download-client surface. The layer that was never extracted is the
configurator itself.

**C15 is live and genuinely unsafe.** `buildTemplateVars` in the host-agent entry
point builds a `map[string]string` that is shared by reference with the
orchestrator and with the authentik configurator. Authentik's `PostStart` writes
`TemplateVars["authentikLdapToken"] = ldapToken` while the orchestrator reads
the same map at container-spec render time. There is no lock. It works today
because `dependsOn` happens to order the writes before the reads, and nothing
states that.

From the developer's side: every new app re-derives these conventions from
whatever neighbour it copied, and no check fails when it copies the wrong one.

## Solution

Harness first. Build the enforcement before the fixes, so the fix list is a
verified inventory rather than a recollection, and so nothing drifts back.

**Phase 1: the conformance harness.** A new exported package in the apps module
that every app's test calls with its own configurator under test. It asserts the
things the contract already promises: `Name()` equals the registered node name;
`PreStart` is idempotent; `PreStart` makes no network call; a configurator
survives a `Deps` with nothing in it; a user app does not implement `Remover`
unless it actually tears something down; and the app's constants match its own
`metadata.yaml`. Table-driven over the registry's node-name list, so a new app is
covered the moment it registers. The first run prints the violation list, and that
list is the scope.

**Phase 2: fix the contract so the harness has something true to assert.**
Replace `PreStart`'s bare boolean with a small result struct whose field is named
for the side effect the orchestrator is about to perform: `RestartNeeded`, with
a `Reason` string that lands in the log. Home Assistant's `staleForce` maps onto
`RestartNeeded` unchanged, so its behaviour is preserved exactly and only the
channel it travels on becomes honest. Give the shared template variables a typed
store with a lock and a named setter for the LDAP outpost token, so no
configurator mutates a map the orchestrator is reading.

**Phase 3: delete the copies.** Move the shared Servarr flow into `pkg/servarr`
so sonarr and radarr become thin parameterized wrappers. Put the bootstrap
account behind one helper with one explicit policy for "cannot verify". Make
every registration use a `nodeName` constant and pass port `0`. Replace the
file-mode literals with named policy constants. Delete the seven no-op `Remove`
methods.

Order matters. The harness lands before the fixes so each fix is verified against
a rule that already exists, not against a claim in a doc.

## Commits

Phase 1: the harness. No app behaviour changes in this phase.

1. Create the conformance harness package in the apps module with the metadata
   loader. It reads `metadata.yaml` from the calling package directory and
   unmarshals the fields a configurator's constants have to agree with: `port`,
   `sso.strategy`, `sso.callbackPath`, and each container's `name`, `command`,
   and `volumes`. This is the vaultwarden test's loader, generalised. Add unit
   tests for the loader itself against a fixture.
   → verify: `cd apps && go test ./configtest/...`

2. Add the identity and nil-safety assertions to the harness: `Name()` equals the
   node name the configurator was registered under, and `PreStart` and `PostStart`
   do not panic when `Deps` is the zero value. Wire one app through it, the
   simplest one that already passes, to prove the harness runs.
   → verify: `cd apps && go test ./configtest/... ./navidrome/...`

3. Add the network-purity assertion. The harness builds a `Deps` whose
   `ClientFactory` carries a recording transport, calls `PreStart`, and fails if
   any HTTP request was made. This is the mechanical half of C5: it cannot catch
   a raw `net.Dial`, so the rule is also stated on the interface.
   → verify: `cd apps && go test ./configtest/...`

4. Add the teardown assertion: a user-app configurator must not implement
   `configurator.Remover` unless it appears in the harness's explicit
   teardown-owning list. Run it over all thirteen apps and let it list the seven
   no-ops. Add them to the list as known violations with a TODO naming the next
   commit, so the harness is green and the debt is visible rather than hidden.
   → verify: `cd apps && go test ./configtest/...`

5. Add the idempotency assertion: call `PreStart` twice against the same state
   and require the second call to report no restart. Run it over all thirteen and
   record the failures the same way.
   → verify: `cd apps && go test ./configtest/...`

6. Wire every app's existing test file to call the harness, so all thirteen are
   covered by the same rules. Where an app already has a private metadata loader,
   delete it in favour of the harness's.
   → verify: `cd apps && go test ./...`

Phase 2: the contract.

7. Delete the seven no-op `Remove` methods. The orchestrator type-asserts
   `Remover`, so removing them changes nothing at runtime. Remove the seven
   entries from the harness's teardown allowlist so the rule now holds with an
   empty list.
   → verify: `cd apps && go test ./... && cd services/host-agent && go test ./internal/engine/orchestrator/...`

8. Introduce the typed `PreStart` result in the configurator package: a struct
   with `RestartNeeded bool` and `Reason string`, plus constructors for the two
   cases every app needs. State the contract on it: report `RestartNeeded` when
   the running container cannot pick up the change, never for directory creation
   alone.
   → verify: `cd services/host-agent && go build ./... && go test ./pkg/configurator/...`

9. Move the orchestrator to the new result. It consumes `RestartNeeded` for the
   remove-before-create decision and logs `Reason` alongside it. Update the
   orchestrator's own tests.
   → verify: `cd services/host-agent && go test ./internal/engine/orchestrator/... -count=1`

10. Convert the apps that return a single signal, one commit per app or one commit
    for the group if each conversion is two lines: navidrome, seerr, immich,
    affine, hermes, vaultwarden. Each returns either no-restart or a restart with
    its reason.
    → verify: `cd apps && go test ./...`

11. Convert jellyfin, which has two reasons, plugin install and network change,
    and reports whichever fired.
    → verify: `cd apps && go test ./jellyfin/...`

12. Convert the Servarr family and qbittorrent, which report the
    `EnsureExternalAuth` or config-file result.
    → verify: `cd apps && go test ./sonarr/... ./radarr/... ./prowlarr/... ./qbittorrent/...`

13. Convert Home Assistant last, and preserve its behaviour exactly. The
    `staleForce` probe keeps its exact condition and its exact effect. What
    changes is that it reports `RestartNeeded` with reason "stored proxy trust is
    not live in the running instance" instead of folding into a boolean that reads
    as a file diff. The three other signals keep their own reasons. Do not move
    the probe out of `PreStart`; the timing is load-bearing.
    → verify: `cd apps && go test ./homeassistant/... -count=1`

14. Tighten the harness now that the contract is honest: the idempotency rule
    drops its allowlist and the network-purity rule gains the recreate-reason
    check, that a reported reason is non-empty.
    → verify: `cd apps && go test ./configtest/...`

15. Replace the shared mutable template-variable map with a typed store. It holds
    the static vars and the LDAP outpost token behind a read-write mutex, exposes
    a named setter for the token, and hands the container-spec renderer a
    resolved snapshot. Update the entry point, the system registration, the
    authentik configurator, the orchestrator config, and the wire builder.
    → verify: `cd services/host-agent && go test ./... && cd apps && go test ./authentik/...`

16. Add a harness rule that no configurator receives a mutable map it is expected
    to write into, checked by the wire completeness test rather than by
    convention.
    → verify: `cd services/host-agent && go test ./internal/wire/...`

Phase 3: delete the copies.

17. Extract the shared Servarr configurator into `pkg/servarr`: the directory
    creation and `EnsureWritable` preamble, `EnsureExternalAuth`, the API-key
    publish, the root-folder seed, the download-client wiring, and the category
    check. Parameterise what differs: the app name, the default port, the API
    path, the root-folder mount, the download-category field and value.
    → verify: `cd services/host-agent && go test ./pkg/servarr/...`

18. Reduce `apps/sonarr` and `apps/radarr` to thin wrappers over the extracted
    configurator, keeping their registration and their app-specific constants.
    Move their shared test cases into the shared package's tests and keep the
    app-level tests as the parameterization check.
    → verify: `cd apps && go test ./sonarr/... ./radarr/...`

19. Extract the bootstrap-account helper: the login fast path, the create, the
    verify, and one explicit policy for the unverifiable case. The policy is the
    one already documented in paperless: an unverified internal admin does not
    make the app unusable for SSO users, so log and continue rather than fail
    the node. Give the helper a single account-naming rule and reconcile the
    three schemes onto it, keeping each app's existing username where a rename
    would orphan a real account on an existing install.
    → verify: `cd apps && go test ./jellyfin/... ./immich/... ./navidrome/... ./paperless-ngx/... ./homeassistant/...`

20. Migrate the remaining apps onto the bootstrap helper and delete their local
    copies of the flow.
    → verify: `cd apps && go test ./...`

21. Unify registration: every app declares a `nodeName` constant and passes port
    `0` to its constructor. Home Assistant stops passing `8123`.
    → verify: `cd apps && go test ./... && go vet ./...`

22. Replace the file-mode literals with named policy constants in the managedfile
    package: one mode for a file that carries a credential and one for a file the
    app reads that carries none. State the rule where a new app will find it.
    → verify: `cd services/host-agent && go test ./pkg/managedfile/... && cd apps && go test ./...`

23. Add the metadata cross-checks to the harness for every app: the constructor's
    default port equals `metadata.yaml`'s `port`, the config file name equals the
    mount destination in the container definition, and the callback path equals
    `sso.callbackPath`. Replace paperless' line-splitting loader with the harness
    loader.
    → verify: `cd apps && go test ./configtest/...`

24. Update the ledger and the contributing guide: mark C5, C6, C7, C10, C11, C14,
    C15 and the C3 regression closed, record the harness as the enforcement, and
    point a new app at it.
    → verify: `npm run check:docs-links && npm run lint:prose`

Final gates.

25. Run the whole fast tier and fix whatever it finds.
    → verify: `./bloud validate --tier fast`

26. Run the race detector over the packages this refactor touches, including the
    new template-var store.
    → verify: `cd services/host-agent && go test -race ./internal/engine/orchestrator/... ./internal/catalog/... ./pkg/configurator/...`

## Decision Document

- The conformance harness is a new exported package in the apps module, not a
  host-agent package. It reads each app's own `metadata.yaml` from the calling
  package directory, which is how the vaultwarden test already does it. The apps
  module cannot import `internal/catalog`, so the harness carries its own mirror
  of the metadata fields it needs rather than reaching across the boundary.
- `PreStart` returns a result struct, not a bare boolean. The field is
  `RestartNeeded`, named for the side effect the orchestrator performs, with a
  `Reason` string that goes to the log. There is deliberately no `ConfigWritten`
  field: nothing consumes a file-diff signal that is separate from the recreate
  decision, and adding one would be inventing a field to have somewhere to put a
  bool.
- Home Assistant's stale-trust probe stays in `PreStart` and keeps its exact
  condition and effect. The user's constraint was behaviour preservation, not
  relocation. Only the reporting channel changes.
- The template variables become a typed store with a read-write mutex and a named
  setter for the LDAP outpost token, replacing the map shared by reference. The
  renderer receives a resolved snapshot, so it never iterates a map another
  goroutine can write.
- The Servarr extraction goes into the existing shared `pkg/servarr`, which
  already owns the client, config, and download-client surface. The new shared
  piece is the configurator flow on top of it, parameterised by the five things
  that differ between the apps.
- Bootstrap accounts get one helper with one policy for the unverifiable case:
  log and continue, never fail the node. Existing usernames are kept where a
  rename would orphan a real account on an existing install; the naming rule
  governs new apps rather than churning live data.
- `Remover` stays optional and the harness enforces that a user app either omits
  it or actually tears something down. The orchestrator's type assertion is
  unchanged.
- Registration standard is a `nodeName` constant and a port of `0`, with the
  default resolved inside the constructor.
- File modes become named policy constants in the managed-file package rather
  than per-app literals.

## Testing Decisions

A good test here asserts externally observable behaviour, not the shape of the
configurator. The harness never inspects a private field or a call graph. It
calls the public contract and checks what a caller would notice: the reported
restart decision, whether a network call happened, whether a second pass changed
anything, whether the app's own metadata agrees with its code.

The harness tests all thirteen user apps through one table over the registry's
node-name list. Coverage is structural: registering an app subjects it to the
rules, so a new app cannot ship without them.

Prior art, all already in this repo:

- `apps/registry_test.go` `TestRegisterAll` and `TestNodeNamesStable`: the same
  idea one level up, a table over the catalog that fails when the wiring lies.
- `apps/vaultwarden/configurator_test.go`: loads its own `metadata.yaml` with
  `yaml.Unmarshal` and asserts the configurator's constants match. This is the
  pattern the harness generalises.
- `services/host-agent/internal/wire/shape_test.go`: pins the orchestrator's
  node convention against the catalog.
- `npm run check:image-pins`: a cross-file consistency check promoted to a
  gate, the same shape of rule as the metadata cross-checks.
- `apps/paperless-ngx/configurator_test.go` `TestMetadata_MatchesRegistration`:
  the intent was right and the mechanism was wrong. Its line splitter cannot see
  nested keys. It is replaced, not extended.

Transient-versus-terminal error behaviour stays pinned where it already is: the
contract on `PostStart` says an error is terminal and the configurator resolves
transients itself. The harness adds the `PreStart` counterpart.

## Out of Scope

- The items in the open inventory that are not configurator-layer conformance:
  the built-in primary host persistence gap, the system-app hiding by category,
  the sharing and system module nil wiring, the unhandled `ClearAppDataIntent`,
  the `DeriveSecret` literal fallback, the hand-assembled Traefik YAML, the
  `PlanInstall` blocker gap, `primaryContainerNode` ordering, the icon path
  traversal, network and orphan sweeping, and health checks not reaching Podman.
- Relocating Home Assistant's probe to `PostStart`. Rejected in favour of
  preserving behaviour exactly.
- Any change to what the apps actually configure. This refactor moves signals and
  deletes copies. No app ends up configured differently.
- Renaming live bootstrap accounts where a rename would orphan an existing
  install.
- The frontend, the CLI, and the packaging.
- Integration-tier and Playwright changes. The existing suites stay as the
  behavioural gate; the harness is a fast complement, not a replacement.

## Further Notes

The audit that produced the C-numbers counted eight apps. There are now thirteen
user apps and the surface grew 62 percent without any of the new apps being
audited against the contract. That is the actual argument for a harness over a
checklist: a checklist records what someone looked at, a harness keeps looking.

The C3 regression is the strongest signal in the whole inventory. The ledger
says the no-op `Remove` methods were deleted in 2026-09-20. Seven came back
within days, in apps written after the fix, because the interface made `Remover`
look like a required part of the shape rather than an opt-in. Deleting them again
without the harness to hold the line would produce the same result a third time.
