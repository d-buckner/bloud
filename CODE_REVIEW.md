# Bloud — Readability & Maintainability Review

Reviewed the full main tree: Go backend (`services/host-agent/internal`, `pkg`, `cmd`, `apps/`), CLI (`cli/`), Svelte/TS frontend (`web/src`, `packages/ui`), and e2e tests. ~35k LOC Go + ~9.4k LOC frontend.

## Verdict

**Overall: well-above-average quality.** The Go backend is clean, well-commented, consistently early-returning, and errors are wrapped with `%w`. Zero TODOs/FIXMEs, no commented-out code, heavy test coverage. The frontend is where the discipline slips: duplicated error handling, dead code, and one genuine Svelte 5 misuse. The CLI has the most duplication. Findings below are grouped by impact.

---

## 🔴 High impact

### 1. `$derived(() => ...)` misuse — memoization silently broken
`services/host-agent/web/src/routes/catalog/+page.svelte:29-35, 38-57`
- `$derived(() => {...})` captures the **function** as the derived value, not its result. The template then calls `categories()` / `filteredApps()` (`+page.svelte:135,144,157,164`) which re-executes the whole filter/category computation on **every render**, and `$derived` never tracks dependencies at all.
- Fix: `let categories = $derived.by(() => {...})` and reference without `()` in the template. This is the single highest-value one-line fix in the frontend — it also makes `categories()` run twice per render (header pills + `{#each}`).

### 2. Hand-rolled `strings.Contains` + string-matching errors for HTTP status
`services/host-agent/internal/api/apps_module.go:338-350` (`errContains`) and callers at `:270`, `remote_apps_module.go:141`
- Reimplements `strings.Contains` with a manual index loop.
- Worse: HTTP status (404 vs 503) is derived by substring-matching `err.Error()`. Any error containing "not found" (e.g. a DB error deep inside) silently becomes a 404. Use sentinel errors (`errors.Is`) or typed errors instead.

### 3. Interface-then-cast pattern — interfaces add nothing
`services/host-agent/internal/api/router.go:158-159, 213, 220-222, 229, 231-233` (10 casts)
- Every module is constructed via `NewXxxModule(...)` returning an interface, then immediately cast back `.(*appsModule)` because `NewXxxRouter` and `SetAppsDir` require the concrete type. The interface indirection is dead weight — either make the router/ctor take the concrete type, or put `SetAppsDir` on the interface.

### 4. Dead frontend code (≈800 lines + an entire package)
- Unreferenced components: `AppMenu.svelte` (173), `HealthPopover.svelte` (238), `InstalledAppRow.svelte` (113), `LogsModal.svelte` (173). This also makes `VirtualLogList.svelte` and `api/logs.ts` production-dead.
- `packages/ui` (`@bloud/ui`) is entirely unreferenced — including a second, near-identical `Button.svelte` (`packages/ui/src/lib/Button.svelte:1-101` vs `web/src/lib/components/Button.svelte`). Duplicated component styles will drift. Delete one package.
- Unused exports: `installedNames`/`appExists`/`getApps` (`stores/apps.ts:33,55,62`), `isHttpError`/`isUnauthorized` (`clients/httpClient.ts:96,107`), `getAllWidgetIds` (`widgets/registry.ts:70`), several unused types in `types.ts`.

### 5. CLI: ignored errors hiding real failures
`cli/dev.go:73, 118, 129, 163, 186, 200, 316, 323, 380, 396`
- `cmdLogs` (118), `cmdAttach` (129), `cmdServices` (163) swallow genuine failures; `cmdStop` prints an unconditional `"Stopped"` at `:74` even when the pkill/systemctl failed.
- `cli/validate.go:655, 661`: failed `git diff --cached` silently yields an empty staged-file list and the run reports success.
- `cli/e2e_lifecycle.go:470, 489, 505, 538`: cleanup failures invisible on the failure-path code path.

### 6. QuickNotes debounce timeout never cleared
`services/host-agent/web/src/lib/widgets/QuickNotes.svelte:21-37` — `setTimeout` can fire and write to localStorage after the widget unmounts. Clear it in the `$effect` cleanup.

### 7. Optimistic-status revert heuristic is wrong
`services/host-agent/web/src/lib/services/appFacade.ts:73-77` — on uninstall failure, status is reverted to `AppStatus.Running` even if the previous status was `Stopped`/`Error`. Capture the pre-update status before the optimistic write.

### 8. `strings.Fields` breaks quoted manifest args (latent bug)
`cli/validate.go:583` — any manifest command containing spaces inside a quoted argument (paths with spaces) will be split incorrectly.

---

## 🟠 Medium

### Go backend
- **`scanApp` / `scanAppRow` are ~60 lines of identical logic** — `store/apps.go:285-361`. `*sql.Rows` and `*sql.Row` share `Scan`; extract one helper over a `Scanner` interface.
- **Orchestrator stores config twice** — `orchestrator.go:104-127`: `config OrchestratorConfig` field *plus* copied fields (`appStore`, `tailnetStore`, …). The two sources can drift (e.g. `o.config.Containers` at `:597` vs copied fields elsewhere). Pick one access path.
- **`secrets.Manager.Get(name)` stringly-typed switch is dead complexity** — `secrets/manager.go:286-304`. Zero external callers; only the 7 typed getters use it. Drop the generic `Get` and the `AppSecrets`/`SetAppSecret` string-keyed switches (355-365, 382-391) in favor of typed accessors.
- **Test fake shipped in production file** — `settings_module.go:503-579`: `FakeSettingsAuthentikClient` (75 lines) lives in non-test code and is exported. Move to `_test.go`. Related: `authentikClientIsAvailable` (`:187-192`) runtime-asserts an ad-hoc `interface{ IsAvailable() bool }`; just add `IsAvailable() bool` to `AuthentikUserManagerInterface` (the fake already implements it).
- **`CreateUserRequest` vs `createUserRequest`** — `settings_module.go:622-625` vs `633-638`: two near-identical user-creation structs, one exported one not. Merge.
- **`buildAppState` duplicated** — `cmd/host-agent/configure.go:267` re-implements `orchestrator.go:841`'s version. Share it.
- **Primitive-obsession parameter lists** — `configure.go:133,180,206,257`: `runPreStart` takes 8 args, `runPostStart`/`runReconcile` 7. A small `AppContext`/`ConfigureDeps` struct would halve the churn.
- **Magic file-extension heuristic** — `orchestrator.go:627`: a 4-way `strings.HasSuffix` chain to decide file-vs-dir mounts. Extract `isFileMount(source string) bool`.
- **`buildIntegrationConfig` has a dead param** — `pipeline.go:67` always passes `nil` for `userChoices`. Remove it or document why.
- **Deep nesting + duplicated block** — `system_module.go:193-245`: 4-level nesting where the `else` branch (~20 lines) re-implements the label-sort/edge-build loop of the `if` branch. Extract `buildGraphEdges(app, def)`.
- **`regexp.MustCompile` per call** — `settings_module.go:651`. Hoist to a package-level `var`.
- **`initAuthHelper` runs eagerly despite "lazy" intent** — `router.go:139-142`: `SetEnsure(...)` is immediately followed by `Set(initAuthHelper(...))`, so the "will be initialized on first request" path (`:420`) never actually lazily happens. Either drop the eager call or keep it and fix the comment.

### Frontend
- **Error-message extraction duplicated 8+ times** in two competing idioms: `'message' in err ? ... : fallback` (`settings/+page.svelte:79-81,99-101,115-117,129-131,153-155,170-172`, `community/+page.svelte:93-97`, `developer/+page.svelte:317-322`) and `err instanceof Error ? ...` (`AppDetailModal.svelte:46`, `ShareModal.svelte:106`, `AddSharedAppModal.svelte:85,99`, `catalog/+page.svelte:64`). One `errorMessage(err, fallback)` util.
- **Status literals bypass `AppStatus`** — `poller.ts:14`, `AppTile.svelte:17`, `AppDetailModal.svelte:18-20`, `CatalogAppCard.svelte:13-15`, `InstalledAppRow.svelte:21` use raw `'installing'` etc. while `+page.svelte` and `appFacade.ts` use the enum correctly. Extract shared `isInstalled(status)`/`isTransitioning(status)`.
- **Raw `fetch` bypassing the shared client** — `+layout.svelte:40,51`, `SetupWizard.svelte:31,58`, `AppDetailModal.svelte:37`, `poller.ts:25`, `layoutClient.ts:18`, `SystemStats.svelte:13`, `Storage.svelte:24` all re-implement what `httpClient.request` already does, producing inconsistent error surfaces.
- **`grid.ts:51-94`** — `addWidget`/`toggleWidget` build the identical element object (incl. the `?? 2` default). Extract `makeWidgetElement()`.
- **`handleAppClick` triple-guard** — `+page.svelte:48-59`: flatten into one guard against a status `Set` (like `poller.ts:14`).
- **`GridStackGrid.svelte:119-141`** — 3-level nested if/else inside an if; use `continue` guards.
- **Overlapping user types** — `+layout.svelte:15-19` `AuthMeResponse`, `Sidebar.svelte:6-9` `User`, `userClient.ts:4-10` `ManagedUser`, `stores/user.ts:5-8` `CurrentUser`. Consolidate; same for the duplicate `SetupStatus` (`+layout.svelte:10-13` vs `SetupWizard.svelte:4-7`).
- **`httpClient.ts`** — `Record<string, any>` in `RequestOptions`/`post`/`put`/`patch` (:13,68,75,82); `return undefined as T` at `:44` lies about the return type (should be `T | undefined`).
- **Spinner CSS copy-pasted** in 4+ components (`SetupWizard.svelte:269-282`, `+layout.svelte:106-119`, `AppTile.svelte:78-91`, `CatalogAppCard.svelte:165-177`).

### CLI
- **`validate.go` epilogue duplication** — the failure block (exit-code + confidence + ledger write) repeats 6× (`:354-359,376-381,401-408,438-445,456-466,521-527`) and the ledger-finish block repeats ~5×. Extract `failIntegration(result, reason)` and `finish(result, code)`.
- **`installApp`/`uninstallApp` are twins** — `dev.go:261-288` vs `:438-465`. Extract `callAppAPI(verb, appName, acceptCodes...)`.
- **Two divergent VM-status checks** — `dev.go:84-94` does brittle `strings.Contains` on `limactl list --json`; `validate.go:544-567` correctly `json.Unmarshal`s it. Reuse the latter.
- **`"bloud-dev"` hardcoded ~8×** in `validate.go:343-483` while `dev.go:46-51` `limaInstance()` reads the env var. Same for `"localhost:3000"` (~15×) and port `3000` — name them.
- **`cmdDev` is a 136-line monolith** (`dev.go:292-428`) with a 9-line inline shell script (`:316`). Extract `buildHostAgent`/`buildFrontend`/`deployArtifacts`.
- **`runIntegrationTier` is 213 lines** of repeated `limactl shell` + failure-epilogue blocks (`validate.go:324-537`). Per-step helpers would halve it.

---

## 🟡 Low / nits

- `cli/vm/detect.go` is **empty**; `cli/vm/preflight.go` (whole file) and `vm.LocalExecStream` (`exec.go:20-25`) are **unused**.
- `cmdRebuild` is a vestigial stub (`dev.go:150-155`) still wired into dispatch.
- `warn()` / `colorYellow` dead in `main.go:14,165-167`; `printUsage` is a wall of `fmt.Println` (`main.go:119-159`) → raw-string const.
- `main.go:58-61`: missing command prints usage and exits **0** (conventional CLIs exit non-zero on misuse).
- `main.go:66-69`: `"setup"` special-cased *outside* the dispatch switch for no evident reason.
- `shellQuoteArg` (`dev.go:146-148`) and `shellQuote` (`validate.go:539-541`) are identical — share one.
- `e2e_lifecycle.go:541-638`: 8 package-level `var remoteXxxScript` never reassigned → should be `const` (regexp at `:46` correctly stays `var`).
- `e2e.go:26-39` inlines a Playwright invocation that `runPlaywright` (`:42-53`) already does — and the two differ in `cmd.Env`.
- `parseSQLiteTime` (`store/apps.go:272-283`) silently returns zero-time on parse failure.
- `Sidebar.svelte:71` hardcodes `v0.1.0`; `settings_module.go` mixes exported/unexported request types; `validate.go:495-502/596-605` double-nest exit-code extraction.
- `orchestrator.go:527` / `pipeline.go:482` / `init_secrets.go:25` `else`-after-return cases: harmless, but trivially invertible to early-return style.

## ✅ What's genuinely good

- **Doc comments**: `orchestrator.go:83-103` (lifecycle/error-handling contract), `graph.go:1-5`, `configurator/interface.go` — better than most production code.
- **Early returns + error wrapping (`%w`)** are the norm in the Go core (`orchestrator.go`, `graph.go`, `apps_module.go`, `pipeline.go`).
- Clean intent pattern (`intent.go`) with sealed interface + compile-time assertions; the app configurators are well-factored into small named methods (jellyfin's 978 lines are ~30 focused functions).
- `catalog/plan.go` / `graph.go` are models of readable dependency logic; `traefikgen` uses a consistent, deterministic builder.
- The `module → handler-factory` API convention (`NewXxxRouter`, methods returning `http.HandlerFunc`) is consistent.

**Priority order if you act on this**: (1) fix `$derived.by` in the catalog page, (2) `errContains` → sentinel errors, (3) delete the dead frontend components + `packages/ui`, (4) `failIntegration`/`finish` helpers in `validate.go`, (5) then the medium bucket.
