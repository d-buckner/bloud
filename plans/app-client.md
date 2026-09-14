# Design: App Client — structured HTTP, assets, and retries for configurators

**Issue:** [#70 — Add app client into framework](https://github.com/d-buckner/bloud/issues/70)
**Status:** Proposed (design; no code landed)
**Date:** 2026-09-13
**Scope:** `services/host-agent/pkg/*`, `apps/*/configurator.go`, `services/host-agent/pkg/authentik`, wiring in `pkg/configurator/factory.go` + `internal/appconfig`.

---

## 1. Problem

Configurators are the only place Bloud holds app-integration knowledge, and today that
knowledge is written as hand-rolled HTTP. Every app re-implements request construction,
status handling, retries, token plumbing, and archive installation. The issue calls out
exactly this: *"configurators implementations have a large amount of adhoc API calls…
it'd be nice to have a client abstraction to cleanly model this in one place."*

Measured against the tree as it stands (non-test files):

|File|`http.NewRequest`|`io.ReadAll`|JSON marshal/unmarshal|
|---|---|---|---|
|`apps/jellyfin/configurator.go`|15|13|10|
|`apps/homeassistant/configurator.go`|6|4|10|
|`apps/navidrome/configurator.go`|5|4|7|
|`apps/immich/configurator.go`|3|3|3|
|`apps/affine/configurator.go`|3|4|3|
|`apps/*` subtotal (5 apps)|**32**|**28**|**33**|
|`services/host-agent/pkg/authentik/client.go`|**60**|**55**|**61**|
|**Total**|**92**|**83**|**94**|

Measured per-function before/after cost of the abstraction (real pairs from this repo):

|Unit|Today|With a client|
|---|---|---|
|`jellyfin.setStartupConfiguration`|21 lines|5 lines|
|`jellyfin.getSystemInfo`|31 lines|3 lines|
|`jellyfin.waitForStartupWizardReady`|36 lines|8 lines|
|`jellyfin.ensureLDAPPlugin` (download+verify+unpack)|105 lines|~14 lines|
|`homeassistant.ensureOIDCComponent` (same shape, a 2nd copy)|122 lines|~16 lines|
|`homeassistant.waitForProxyTrust`|24 lines|7 lines|
|`immich.waitForServer` / `affine.waitForServer`|29 / 29 lines (near-identical)|4 lines each|

**Consequences that are not style nits:**

1. **One transient failure bricks an install.** `collectWorkForLevel`
   (`internal/orchestrator/orchestrator.go:728`) treats `ERROR` as terminal: *"ERROR is
   terminal — never retry without an explicit status reset."* A single `503 Server is
   loading` outliving an app's hand-rolled attempt cap lands the node in `ERROR` until the
   user reinstalls. Configurators have compensated with oversized defensive retry loops
   (jellyfin has three separate ones in one function, `configurator.go:315-376`), each with
   its own attempt count, sleep, and cancellation check. The retry policy is therefore
   inconsistent per app and untestable per app.
2. **No request timeouts on most calls.** Jellyfin, Immich, AFFiNE and the Authentik client
   use `http.DefaultClient`, which has **no timeout**. A wedged app connection blocks
   `PostStart` until the outer budget expires.
3. **Cancellation is ignored or over-corrected.** Jellyfin detaches with
   `context.WithBackground`-equivalent (`configurator.go:291`, `context.Background()`), HA
   with `context.WithoutCancel` (`configurator.go:200`). Both **silently defeat shutdown**:
   `orchestrator.Stop()` cannot cancel an in-flight `PostStart`. Meanwhile the stated reason
   for the detach — "the pass context is cancelled when the pass completes" — is **not true
   in the current code**: the pass context comes from `Start()`'s context
   (`orchestrator.go:471-489`) and is only cancelled by `Stop()`.
4. **Idempotency is a string match.** `strings.Contains(strings.ToLower(b), "already has an
   admin")` (immich:261), `strings.Contains(string(b), "First user already created")`
   (affine:198), `resp.StatusCode == http.StatusUnauthorized` meaning "wizard already
   finished" (jellyfin:477), `403 → treat as success` (HA:982). These are per-copy,
   untypeable, and break silently when upstream wording changes.
5. **Auth is re-derived at every call site.** Jellyfin repeats this literal string in **7**
   methods (`configurator.go:727,758,917,946,971,1001,1030`):
   `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0"`.
   Nobody re-logs in on a `401`; tokens are re-fetched or leaks persist.
6. **Static-file installation is copy-pasted.** `jellyfin.ensureLDAPPlugin` (105 lines) and
   `homeassistant.ensureOIDCComponent` (122 lines) are the same program: GET → stream-sha256
   → temp file in destination dir → zip-slip-guarded unpack into staging → sentinel check →
   `RemoveAll` + `Rename`. Two copies today; both drift independently.
7. **The existing helper package already failed this test.** `pkg/configurator/health.go`
   ships `WaitForHTTP`, `WaitForHTTPWithAuth`, `WaitForTCP`, `WaitForOpenIDConfig`,
   `ShouldWaitForSSO` — **zero call sites anywhere in the repo, tests included**. Only
   `WaitForSSOReady` is used (one CLI path). Configurators rolled their own waits instead of
   using the helper because the helper could not express "retry 5xx, 401 means already done,
   keep the last good read". That is the design brief.
8. **Debug noise shipped to production.** `apps/jellyfin/configurator.go` carries 14
   `DBG`-prefixed log lines (`:300-437`, across `postStart` and `getSystemInfo`) that were
   diagnostic scaffolding for issue #71 and never came out.

---

## 2. Taxonomy: what configurators actually do

Every HTTP-adjacent behavior in the current tree falls into eight shapes. The abstraction
must cover these and nothing more.

| # | Shape | Sites | Notes |
|---|---|---|---|
| 1 | **Readiness wait** (poll until the listener/API answers) | immich `waitForServer`, affine `waitForServer`, HA `waitForAPI`, jellyfin `waitForStartupWizardReady` | 4 app loops + 4 unused `health.go` helpers; differ only in URL, "ready" predicate, deadline |
| **2** | **Plain JSON verb with status expectations** | ~23 of the 32 configurator requests | `POST json → 200/204`, `GET → decode` |
| 3 | **Value-settle wait** (poll until an observed *value* stabilises) | jellyfin wizard-completed re-poll (`:358-375`), authentik `EnsureLoginConfiguration` apply→verify-blueprint (`:1339-1392`), authentik `EnsureBranding` brand-migration wait (`:1571-1593`), HA `waitForProxyTrust`, `waitForOIDCReady`, affine `waitForOIDCPreflight` | The hard ones: the app oscillates or an external actor reverts the change |
| 4 | **Auth bootstrap + token reuse** | jellyfin `authenticate`, HA `ensureOnboarded`→`exchangeAuthCode`, immich `login`, navidrome `login`/`createAdmin`, authentik bearer token | Login once, sign many calls; **no** 401-refresh anywhere |
| 5 | **Compare-and-apply (ensure)** | jellyfin `configureLDAP` (get→diff→set), authentik `Ensure*` (~20 methods), navidrome `syncUsersFromAuthentik` (list→diff→create-each) | The dominant write pattern; must be a no-op when converged |
| 6 | **Rendered config-file write with change detection** | immich `renderConfigFile`, affine `renderConfigFile`, HA `managedBlock`+`mergeManagedBlock`, jellyfin `xmlutil` network.xml | `changed` feeds the orchestrator's container-recreate signal |
| 7 | **Downloaded + checksummed archive** | jellyfin LDAP plugin, HA `hass-oidc-auth` | 227 lines across two near-identical implementations |
| 8 | **Embedded/copied static file** | authentik `auth.yaml` from `appsDir`, `go:embed` python scripts | Host-path coupling that `go:embed` would remove |

Two extras worth naming because the framework should own them, not the app:

- **Container exec** — authentik shells `podman exec … ak shell -c <script>`
  (`apps/authentik/configurator.go:23-40`) instead of using the injected
  `container.Runtime.Exec` (`internal/container/runtime.go:68`).
- **Cross-service calls** — navidrome reads the Authentik API token from disk
  (`configurator.go:303`) and calls Authentik directly.

---

## 3. Goals / non-goals

**Goals**

- G1 — A call site reads as *intent* (verb, path, expected outcome), not transport mechanics.
- G2 — One retry policy, one classifier, one error format, one logger shape — testable in
  isolation from any app.
- G3 — Idempotency is *declared* (status set / predicate), never string-guessed unless the
  upstream API gives no other signal, in which case the escape hatch is explicit and
  documented.
- G4 — Every request has a timeout, respects ctx cancellation, and can be fast-forwarded in
  tests (injectable sleeper).
- G5 — Static assets are declarative: source + checksum + destination + sentinel. One
  hardened implementation for the download/verify/unpack/atomic-install path.
- G6 — Apps stay self-contained: app-specific wire types and vendor quirks stay in `apps/<name>/`.
  The framework does **not** learn what a Jellyfin library or an AFFiNE owner account is.
- G7 — Cheaper to add the next app than the last one.

**Non-goals**

- N1 — No generic workflow/retry engine (explicit tech-debt non-goal,
  `docs/operations/tech-debt.md:135`).
- N2 — No data-driven app API description (`metadata.yaml: api:` blocks). The quirks are
  code-shaped; Go stays the modeling language. Revisit only if we ever need to render app
  APIs in the UI.
- N3 — No third-party HTTP/retry dependency. Stdlib only.
- N4 — Not a client for *Bloud's own* API (the :3000 surface) — different consumers,
  different contract.
- N5 — No behavioral change to install/reconcile semantics beyond making existing behavior
  consistent and correctly bounded.

---

## 4. Shape of the solution

Three pieces, two new packages:

```
apps/<name>/api.go            per-app typed client: wire types + declared outcomes
      │                       (the "one place" the issue asks for; vendor quirks live here)
      ▼
pkg/appclient                 transport · retry · error classification · waits · auth
      │                       (knows HTTP, nothing about apps)
      ├──────────────────────────────┐
      ▼                              ▼
pkg/appasset                 static file/archive install: fetch/verify/stage/commit
      │                       (knows archives, nothing about apps)
      ▼
configurator.Deps ─────────── framework injection: shared transport, shared policy,
(host-agent → factories)      shared asset cache, PostStart budget
```

- **`apps/<name>/api.go`** — split from `configurator.go`: the typed surface for that app
  (`GetSystemInfo`, `CreateAdmin`, `SetLDAPConfig`), each method 2–6 lines of declared
  intent. The configurator keeps the *orchestration* of those calls.
- **`pkg/appclient`** — lives in the host-agent module so the `apps` module can import it
  exactly as it imports `pkg/xmlutil` today (module cycle already solved by `replace`).
- **`pkg/appasset`** — same placement; depends on `appclient` only for the fetch+retry.

---

## 5. `pkg/appclient` — API

### 5.1 Construction

```go
package appclient

// Spec configures one client. Zero values are valid: New fills defaults.
type Spec struct {
    Name    string // node/log identity, e.g. "jellyfin"
    BaseURL string // "http://localhost:8096"
    // BaseURLFn overrides BaseURL when set — for host-set-aware callers
    // (same reason Deps.PrimaryBaseURL is a func today).
    BaseURLFn func() string

    Timeout time.Duration // per-request; default 15s
    Retry   RetryPolicy   // default DefaultRetry
    Headers map[string]string

    // Auth applies credentials. Exactly one of these is normally set.
    StaticAuth func(*http.Request)     // static headers (no token lifecycle)
    Tokens     *TokenSpec              // managed token with 401 refresh

    Transport *http.Transport // nil → process-shared transport (see §6)
    Logger    *slog.Logger    // nil → slog.Default()

    // FollowRedirects defaults true. HA's trust/redirect probes need
    // ErrUseLastResponse; set false to see the 3xx itself.
    FollowRedirects *bool
}

func New(spec Spec) *Client

// Sleeper lets tests collapse backoff to zero (HA currently does this by hand
// with a pollInterval field — same lever, one place).
func (c *Client) WithSleeper(fn func(time.Duration)) *Client
```

### 5.2 Call builder + terminal operations

```go
func (c *Client) GET(path string) *Call
func (c *Client) POST(path string) *Call
func (c *Client) PUT(path string) *Call
func (c *Client) PATCH(path string) *Call
func (c *Client) DELETE(path string) *Call

// --- request body / modifiers ---
func (x *Call) JSON(v any) *Call                    // sets Content-Type: application/json
func (x *Call) Form(v url.Values) *Call             // x-www-form-urlencoded
func (x *Call) Body(raw []byte, contentType string) *Call
func (x *Call) Query(k, v string) *Call
func (x *Call) Header(k, v string) *Call
func (x *Call) Timeout(d time.Duration) *Call       // overrides Spec.Timeout
func (x *Call) Anonymous() *Call                    // do not apply Tokens

// --- outcome contract (this is where idempotency is declared) ---
func (x *Call) OK(status ...int) *Call              // exact accepted success codes
func (x *Call) AlreadyDone(status ...int) *Call     // success with changed=false
func (x *Call) AlreadyDoneFunc(p func(status int, body []byte) bool) *Call
func (x *Call) RetryStatus(status ...int) *Call     // add to the transient set
func (x *Call) NoRetry() *Call

// --- terminal operations ---
func (x *Call) Do(ctx context.Context) ([]byte, error)         // no decode
func (x *Call) DoInto(ctx context.Context, out any) error      // JSON decode
func (x *Call) Ensure(ctx context.Context) (changed bool, err error) // changed = not AlreadyDone
```

Default outcome rule when nothing is declared: **2xx is success, everything else is an
error** — same as today's code, so migration is a no-op for the happy path.

### 5.3 Retry policy + classification

```go
type RetryPolicy struct {
    MaxAttempts int           // 0 = unbounded (bounded by Deadline/ctx)
    Deadline    time.Duration // wall clock; 0 = ctx only
    Initial     time.Duration // default 500ms
    MaxInterval time.Duration // default 5s
    Factor      float64       // default 2.0
    Jitter      float64       // 0..1 fraction of the interval, default 0.2
}

var DefaultRetry = RetryPolicy{MaxAttempts: 5, Deadline: 30 * time.Second,
    Initial: 500 * time.Millisecond, MaxInterval: 5 * time.Second, Factor: 2, Jitter: 0.2}

// WaitPolicy is the default for calls marked with Ready(): long, generous,
// because the job is "wait out an app boot", not "paper over a blip".
var WaitPolicy = RetryPolicy{MaxAttempts: 0, Deadline: 3 * time.Minute,
    Initial: time.Second, MaxInterval: 5 * time.Second, Factor: 1.5, Jitter: 0.1}
```

Transient classification (retried under any policy unless `NoRetry()`):

| Observation | Transient? |
|---|---|
| transport error (refused, reset, EOF, DNS) | yes |
| `408 Request Timeout`, `425`, `429 Too Many Requests` | yes (honors `Retry-After`) |
| `500`, `502`, `503`, `504` | yes |
| any other status | no — fail fast with `HTTPError` |
| `ctx` cancelled/deadline | no — return immediately, wrapped |

`Retry-After` on 429/503 overrides the computed backoff (delta-seconds and HTTP-date both
parsed; clamped to `MaxInterval × 3`).

### 5.4 Errors

```go
type HTTPError struct {
    Method  string
    URL     string
    Status  int
    Body    []byte // capped at 512 bytes; "… (+N bytes)" suffix when truncated
    Attempt int    // 1-based; >1 means it was retried
}

func (e *HTTPError) Error() string   // `jellyfin: POST /Startup/User → 503 (attempt 4): <body>`
func (e *HTTPError) IsTransient() bool

func StatusOf(err error) int   // 0 when err is not an HTTPError
func BodyOf(err error) []byte  // nil otherwise
```

Apps stop writing `fmt.Errorf("unexpected status %d: %s", …)` and instead either declare
`OK(...)` or inspect with `errors.As`. Existing wrapping stays compatible (`%w`).

### 5.5 Auth: token spec with 401 refresh

```go
type TokenSource interface {
    Token(ctx context.Context) (string, error)
    Invalidate() // force refetch on next use
}

// Cached wraps a fetch func with memoization + single-flight.
func CachedToken(fetch func(ctx context.Context) (string, error)) TokenSource

type TokenSpec struct {
    Source TokenSource
    Header string // default "Authorization"
    Format string // printf template, default "Bearer %s"
}
```

This one type covers all four observed auth header dialects:

| App | Header / Format |
|---|---|
| immich, HA, authentik | `Authorization`, `"Bearer %s"` (default) |
| navidrome | `X-ND-Authorization`, `"Bearer %s"` |
| jellyfin | `Authorization`, `` `MediaBrowser Client="Bloud", Device="Host-Agent", DeviceId="bloud-host-agent", Version="1.0.0", Token="%s"` `` |

Behavior: the token is fetched lazily once, attached to every non-`Anonymous()` call, and on
`401` the client calls `Invalidate()`, refetches **once**, and retries — the behavior no
configurator has today. The login call itself is `.Anonymous()`.

### 5.6 Waits (shape 1 + 3)

A `Call` becomes a wait when it carries a readiness predicate:

```go
type ReadyFunc func(status int, body []byte) bool

func (x *Call) Ready(p ReadyFunc) *Call     // switches the call into wait mode
func (x *Call) Interval(d time.Duration) *Call
func (x *Call) Stable(n int) *Call         // predicate must hold n consecutive polls
func (x *Call) TolerateFailures() *Call    // a successful read earlier makes a later timeout non-fatal

// Terminal wait operation.
func (x *Call) Wait(ctx context.Context) error
```

Wait-mode outcome table (this replaces ~9 bespoke loops):

| Observation | Behavior |
|---|---|
| `AlreadyDone` matched | return `nil` immediately (not converged-by-mutation) |
| `Ready` true on `Stable` consecutive polls | return `nil` |
| `Ready` false | retry |
| transient (per §5.3) | retry |
| non-transient, unexpected status | retry (in wait mode an unexpected answer means "not ready yet"), but surfaced in the final error |
| deadline/ctx | return error naming the probe, attempt count, and last observation |

Convenience predicates:

```go
func StatusIn(code ...int) ReadyFunc
func StatusIs(code int) ReadyFunc
func StatusNot(code int) ReadyFunc
func StatusLT(code int) ReadyFunc            // "anything under 500 means listener is up"
func DecodeInto(out any, cond func() bool) ReadyFunc // value-condition
func JSONHas(key string) ReadyFunc          // {"url": …} present → provider is live
```

`TolerateFailures` exists specifically for the jellyfin case documented at
`configurator.go:348-357`: *"a fresh install reports the wizard as pending… a 503 here must
never fail PostStart… keep the last good read and fall through."* Today that is 28 lines of
loop with a shadowed `info` variable; with the primitive it is a modifier.

### 5.7 Ensure (shape 5)

```go
// Ensure reads current, compares to desired, applies only when different.
// Equal may be nil → canonical-JSON equality.
type EnsureSpec[T any] struct {
    Name    string
    Current func(ctx context.Context) (T, error)
    Desired T
    Equal   func(cur, want T) bool
    Apply   func(ctx context.Context, want T) error
}

func Ensure[T any](ctx context.Context, s EnsureSpec[T]) (changed bool, err error)
```

Justification: the get→diff→set shape occurs ~25 times (jellyfin LDAP config, ~20 authentik
`Ensure*` methods, navidrome sync loop). `Ensure` also gives one place to log
`already converged` vs `applied`, which is exactly the signal the reconciler story needs.

For the **revert-resistant** variant (authentik's blueprint overwrites a patch it just
applied, `client.go:1329-1392`), compose `Ensure` + `Wait`: apply → `Wait(Stable(2))` on
the read-back → if reverted, re-apply, bounded by `WaitPolicy.Deadline`. No new primitive.

### 5.8 Logging / observability

One record per call at `Debug` level:
`{app, method, path, status, attempt, dur_ms, outcome}`.
On error: `Warn` with attempt count and the capped body. Wait completion logs
`waited N attempts / Xs`.

**Rule: no `DBG`-style inline logging in app packages** — the client emits the trace, so
jellyfin's 14 diagnostic lines (`apps/jellyfin/configurator.go:300-437`) are deleted, not
rewritten.

Optional (deferred, §13 Q5): a bounded ring buffer of recent configurator calls exposed via
`GET /api/debug/configurator-calls` for operators. Cheap to add later because all calls
already funnel through one place.

### 5.9 Test hooks

- `WithSleeper(noop)` — collapses all backoff; tests run in milliseconds.
- `RetryPolicy.Deadline` small + `MaxAttempts` explicit → deterministic.
- Tests point the client at `httptest.Server` via `Spec.BaseURL`. **Keep the existing
  httptest style** (all five app test files already do this) — no fake-interface layer, which
  would test less than a real socket does.
- Optional recorder for behavior assertions ("converged install makes **zero** mutating
  calls"): `appclient.WithRecorder(&rec)`, `rec.Calls(method, path)`. Lives in
  `pkg/appclient/apptest` to keep the production package clean.

---

## 6. Framework wiring

Add to `configurator.Deps` (`services/host-agent/pkg/configurator/factory.go:16-42`):

```go
type Deps struct {
    Logger *slog.Logger
    Secrets AppSecretsProvider
    PrimaryBaseURL func() string
    TraefikPort int
    RestartContainer func(ctx context.Context, name string) error

    // HTTP builds clients for this process: shared transport, default policy,
    // shared logger. Zero value is usable (defaults constructed lazily).
    HTTP ClientFactory

    // Assets installs static/downloaded files with the shared content cache.
    Assets appasset.Installer
}

// ClientFactory is a value type; New never panics on a zero Factory.
type ClientFactory struct {
    Transport *http.Transport
    Retry     appclient.RetryPolicy
    Logger    *slog.Logger
}

func (f ClientFactory) New(s appclient.Spec) *appclient.Client
```

`internal/appconfig.AppDeps` (register.go:99) constructs the shared transport once:

```go
transport := &http.Transport{
    Proxy:               http.ProxyFromEnvironment,
    MaxIdleConns:        32,
    MaxIdleConnsPerHost: 8,
    IdleConnTimeout:     60 * time.Second,
    TLSHandshakeTimeout: 10 * time.Second,
    DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
}
```

**Why shared:** today `homeassistant.apiGet` allocates a fresh `http.Client` on every call
(`configurator.go:677,744,911,975,1038`) and others use `http.DefaultClient` — connection
reuse across reconciliation cycles is accidental. One transport gives pooling, one dial
timeout, and one place to change keep-alive behavior for the whole runtime.

**Nil-tolerance:** `ClientFactory{}.New(...)` must work (lazy defaults) — the existing
factory contract is "tolerate nil deps in CLI/test contexts" (`factory.go:20-22`), and a
client that panics there would regress `bloud configure prestart`.

**App-side usage** (`apps/<name>/registration.go` unchanged in spirit):

```go
func init() {
    configurator.MustRegisterFactory("apps-jellyfin", func(deps configurator.Deps) configurator.NodeLifecycle {
        return NewConfigurator(0, deps)
    })
}

func NewConfigurator(port int, deps configurator.Deps) *Configurator {
    if port == 0 { port = 8096 }
    api := deps.HTTP.New(appclient.Spec{
        Name:    "jellyfin",
        BaseURL: fmt.Sprintf("http://localhost:%d", port),
        Timeout: 15 * time.Second,
    })
    return &Configurator{api: api, assets: deps.Assets, logger: deps.Logger.With("app", "jellyfin")}
}
```

Tests override by constructing their own client against `httptest` and injecting it — no
`baseURL`/`baseURLOverride` string fields left in five configurators
(`jellyfin:43`, `homeassistant:74`, plus affine/immich/navidrome URL funcs).

---

## 7. `pkg/appasset` — static files in PreStart

Issue text: *"I'm sure there's a similar challenge with static files in prestart
configurators, but I haven't looked into that."* There is — 227 lines duplicated across two
archive installers, plus four hand-rolled config-render-and-detect paths.

### 7.1 Asset spec

```go
package appasset

type Asset struct {
    Name   string // log + marker identity ("hass-oidc-auth")
    Dest   string // absolute target (dir for Zip, file for File)
    Source Source // exactly one
    Kind   Kind   // File | Zip
    SHA256 string // required for URL sources; empty is a hard error
    Strip  int    // zip leading path components to strip
    // Sentinel: path relative to Dest that must exist after install.
    // Doubles as the default "already installed" check.
    Sentinel string
    // SkipIf overrides the sentinel check (e.g. version-aware skip).
    SkipIf func(dest string) bool
    // Verify runs on the staged tree before commit; failure aborts, nothing lands.
    Verify func(staging string) error
    MaxBytes int64 // download cap; default 64 MiB
    Retry  appclient.RetryPolicy
}

type Source struct {
    URL   string             // remote fetch (retry + cache by SHA256)
    Embed fs.FS              // go:embed in the app package
    Local string             // host path (transitional — see 7.4)
}

type Installer struct{ CacheDir string; Retry appclient.RetryPolicy }
func (in Installer) Install(ctx context.Context, a Asset) (changed bool, err error)
```

### 7.2 Install semantics (one hardened implementation)

1. `SkipIf`/sentinel present → `(false, nil)`. No network.
2. Resolve source:
   - URL: consult content cache `<data>/asset-cache/<sha256>` first; on miss, `GET` with
     retry (transient §5.3 + `Retry-After`), stream to a temp file inside the *destination*
     filesystem, hashing while streaming, capped at `MaxBytes`.
   - Embed/Local: stream from the FS.
3. Verify digest **before** touching the destination. Mismatch → error, temp removed, cache
   poisoned entry deleted.
4. Zip: unpack into `staging` (temp dir in the destination's parent) with the zip-slip guard
   (`filepath.Clean(dest)` must stay under staging — the guard both copies already have).
   Preserve modes, default `0644` when the archive says `0000`.
5. `Verify(staging)` — sentinel + content assertions.
6. `RemoveAll(dest)` → `Rename(staging, dest)`. A failed install never leaves a half tree
   (the property both current copies deliberately implement).
7. Return `(true, nil)`.

Doing the download **with retry** is a new property. Today a transient CDN failure in PreStart
fails the node into terminal `ERROR` (§1.1) on a single bad GET.

### 7.3 Rendered config files

`pkg/managedfile` already has the right primitive (`Write` returns `changed`, atomic via
temp+rename). Add two siblings rather than letting each app re-implement them:

```go
// Render writes deterministic rendered content; changed=false when unchanged.
func Render(path string, mode os.FileMode, render func() (string, error)) (bool, error)

// Block owns one marker-delimited region inside a user-owned file.
type Marker struct{ Begin, End string }
func Block(path string, m Marker, mode os.FileMode, render func() string) (bool, error)
func RemoveBlock(path string, m Marker) (bool, error)
```

`Block`/`RemoveBlock` is the HA pattern (`mergeManagedBlock` + `removeManagedBlock` +
`joinLines`, `homeassistant/configurator.go:584-660`, ~77 lines with the unterminated-region
edge case) generalized and tested once. HA keeps `managedBlock(oidc) string` (its content)
and drops the merge machinery.

### 7.4 Killing `appsDir` coupling

`authentik.ServerConfigurator` reads its blueprint from a host path (`c.appsDir`,
`server_configurator.go:69`). It already uses `go:embed` for its python scripts
(`configurator.go:14-18`) — the blueprint should be embedded too. Effect: the app package
becomes self-contained; `Deps` loses a host-path dependency; and the packaged release's
"Required static assets" line (`specs/spec.md:778`) is satisfied by the binary, not by
directory layout luck.

### 7.5 Provenance

Every pinned remote asset must record, in `apps/<name>/INTEGRATION.md` under **Verified
constants** (convention already exists — `homeassistant/INTEGRATION.md`): upstream URL,
version, sha256, verification date, upstream commit/tag, and the command used
(`curl -sL <url> | sha256sum`). Make it a checklist item in the contributing guide. The
content-addressed cache means a re-verified asset with a new sha simply re-downloads; a
retagged upstream that changes bytes fails loudly instead of installing silently.

---

## 8. Before / after (real code from this repo)

### 8.1 A plain POST (`jellyfin/configurator.go:521-549`, 29 → 5 lines)

**Before**

```go
func (c *Configurator) setStartupConfiguration(ctx context.Context) error {
	url := c.getBaseURL() + "/Startup/Configuration"
	config := map[string]interface{}{"UICulture": "en-US", "MetadataCountryCode": "US",
		"PreferredMetadataLanguage": "en"}
	body, _ := json.Marshal(config)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil { return err }
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req) // no timeout
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
```

**After**

```go
func (c *jellyfinAPI) setStartupConfiguration(ctx context.Context) error {
	return c.cl.POST("/Startup/Configuration").
		JSON(map[string]string{"UICulture": "en-US", "MetadataCountryCode": "US",
			"PreferredMetadataLanguage": "en"}).
		OK(http.StatusOK, http.StatusNoContent).
		Do(ctx)
}
```

### 8.2 A readiness wait (`jellyfin/configurator.go:452-487`, 36 → 9 lines)

```go
// Ready when the wizard endpoint answers JSON; 401 means the wizard is already
// complete (endpoint moved behind auth in Jellyfin 10.11.9+).
func (c *jellyfinAPI) waitForWizardReady(ctx context.Context) error {
	return c.cl.GET("/Startup/Configuration").
		Interval(time.Second).Timeout(60 * time.Second).
		Ready(func(status int, body []byte) bool {
			return status == http.StatusUnauthorized ||
				(status == http.StatusOK && json.Valid(body))
		}).
		Wait(ctx)
}
```

### 8.3 Declared idempotency (affine `configurator.go:189-205`, 17 → 5 lines)

```go
func (c *affineAPI) ensureOwner(ctx context.Context, email, password string) (bool, error) {
	return c.cl.POST("/api/setup/create-admin-user").
		JSON(map[string]string{"name": ownerName, "email": email, "password": password}).
		OK(http.StatusOK, http.StatusCreated).
		// AFFiNE has no dedicated code for this case; documented in INTEGRATION.md.
		AlreadyDoneFunc(func(s int, b []byte) bool {
			return s == http.StatusForbidden && bytes.Contains(b, []byte("First user already created"))
		}).
		Ensure(ctx)
}
```

The string match survives because AFFiNE gives no better signal — but it is now *declared in
the outcome contract*, logged as `already converged`, testable, and confined to one line
with a comment. Immich's 400-string case gets the same treatment.

### 8.4 Trust probe with a hybrid stop condition (`homeassistant/configurator.go:772-795`)

```go
// Anything except a forward-middleware 400 means the running process now honours
// X-Forwarded-*; a refused connection mid-restart is retried, not fatal.
func (c *haAPI) waitForProxyTrust(ctx context.Context) error {
	return c.cl.GET("/api/").
		Header("X-Forwarded-For", xffProbeAddr).
		Interval(2 * time.Second).Timeout(3 * time.Minute).
		Ready(appclient.StatusNot(http.StatusBadRequest)).
		Wait(ctx)
}
```

### 8.5 Archive install (`jellyfin/configurator.go:110-214`, 105 → 13 lines)

```go
func (c *Configurator) ensureLDAPPlugin(ctx context.Context, dataPath string) (bool, error) {
	return c.assets.Install(ctx, appasset.Asset{
		Name:     "jellyfin-ldap-auth",
		Dest:     filepath.Join(dataPath, "config", "plugins", "LDAP-Auth"),
		Source:   appasset.URL(c.pluginURL),
		Kind:     appasset.Zip,
		SHA256:   c.pluginSHA256,
		Sentinel: "LDAP-Auth.dll",
	})
}
```

Home Assistant's variant adds a version-aware skip and a manifest-domain check:

```go
	return c.assets.Install(ctx, appasset.Asset{
		Name:     "hass-oidc-auth",
		Dest:     filepath.Join(customDir, componentDomain),
		Source:   appasset.URL(c.componentURL),
		Kind:     appasset.Zip,
		SHA256:   c.componentSHA,
		SkipIf:   appasset.ManifestVersionEq("manifest.json", oidcComponentVersion),
		Verify:   appasset.ManifestFieldEq("manifest.json", "domain", componentDomain),
		Sentinel: "manifest.json",
	})
```

---

## 9. Design decisions and tradeoffs

| # | Decision | Rationale / rejected alternative |
|---|---|---|
| D1 | Client lives in `services/host-agent/pkg/appclient`, injected via `Deps` | `apps` already imports host-agent `pkg` packages via the existing `replace` cycle. Rejected: a new module (more wiring for no isolation gain); putting it in `apps` (host-agent's own Authentik client couldn't use it). |
| D2 | Stdlib only; hand-rolled exponential backoff + jitter + `Retry-After` | Repo has no retry dependency; the policy is ~80 LoC and fully testable. Rejected: `hashicorp/retry`, `cenkalti/backoff` — a dependency whose whole job is one function we must be able to reason about at the call site. |
| D3 | **Verb retry policy:** `GET` retries by default; mutating verbs retry **only** when the call declares `OK(...)`/`AlreadyDone(...)`, is a `Wait`, or opts in explicitly | Blindly retrying a non-idempotent `POST` risks double-created admins/libraries/providers. The type-level nudge forces the author to state the outcome contract, which is the whole point of the abstraction. Cost: slightly more verbose mutations. |
| D4 | `Client` is a concrete struct; no per-app interface | The repo's `interfaces.go` mock pattern (configurator, authentik) exists to break cycles, not for polymorphism. Apps test against `httptest` over a real socket — strictly more real than a mock. |
| D5 | Outcome contract is a **status set + predicate**, string matching only via declared `AlreadyDoneFunc` | Fixes the class of bug (silent wording drift) while remaining honest about upstream APIs that lack distinct codes. |
| D6 | Per-request timeout default 15s; waits carry an explicit `Timeout` | Kills the `http.DefaultClient`-with-no-timeout failure mode (§1.2) without per-app knobs. |
| D7 | **Delete the per-app detach dance.** Framework guarantees (verified: `orchestrator.go:471-489`) the pass context is *not* cancelled at pass end and *is* cancelled at `Stop()`; apps just use `ctx` | `context.Background()`/`WithoutCancel` in `PostStart` make shutdown uncancellable. Their stated premise is false in current code. Jellyfin's claim that `WithoutCancel` "did not prevent cancellation on Go 1.25" is recorded as an open question (§13 Q1) — with the pass ctx being process-scoped, apps don't need either. |
| D8 | `Ensure[T]` generic helper, not an app-by-app interface | Same 5-line shape ~25×; one implementation, one log line, one test. |
| D9 | Vendor quirks stay in `apps/<name>/api.go`; the framework never learns app semantics | Prevents the client becoming a per-app switchboard. `pkg/authentik` stays the only host-owned service client (Bloud owns that integration) — but it gets **rebuilt on** `appclient` rather than hand-rolling 60 requests (§12 R4). |
| D10 | Content-addressed asset cache under `BLOUD_DATA_DIR` | Repeat installs/reinstalls skip re-download; a broken cache entry is self-deleting (digest mismatch). Enables the air-gapped bundle path later without changing the API. |
| D11 | No `metadata.yaml` API/asset declarations (N2) | Tech-debt non-goal: "no speculative provider abstractions before a concrete consumer needs them" (`tech-debt.md:136`). |
| D12 | `container.Exec` becomes a `Deps` capability so authentik stops shelling `podman` directly | Keeps the orchestrator the only container-effect executor (invariant #1). Transitional slice (§11 S6). |

---

## 10. What each app looks like after

| App | New `apps/<app>/api.go` | Removed |
|---|---|---|
| jellyfin | 15 typed methods, MediaBrowser token via `TokenSpec.Format` template | `getBaseURL`, `baseURL` field, 3 retry loops, `DBG` logs, 7 duplicated auth strings, 105-line plugin installer |
| homeassistant | ~10 methods (API up, onboarding, token exchange, trust probe, OIDC live) | `apiGet` per-call `http.Client`s, `postJSONBearer`, `waitForAPI`, `waitForProxyTrust`, `waitForOIDCReady` bodies, 127-line component installer, `mergeManagedBlock` machinery |
| immich | `waitServer`, `createAdmin`, `login` | `waitForServer`, `baseURL` |
| affine | `waitServer`, `ensureOwner`, `waitOIDCPreflight` | `waitForServer`, `waitForOIDCPreflight` bodies, `baseURL` |
| navidrome | navidrome + authentik clients (`TokenSpec` header `X-ND-Authorization`) | `login`/`createAdmin`/user-list bodies, `baseURL` |
| authentik (host-owned) | client rebuilt on `appclient`: `WithContext` on all 60 requests, shared retry/status handling | 60 hand-built request blocks, hand-rolled retry loops in `EnsureBranding`/`EnsureLoginConfiguration` |

---

## 11. Migration plan

Each slice is independently shippable, keeps `go test ./...` green in `apps` and
`services/host-agent`, and is separately revertable. No slice requires another app to move.

| Slice | Change | Prove |
|---|---|---|
| **S1** — `pkg/appclient` core | `New/Spec/Call`, verbs, `OK`/`AlreadyDone`/`RetryStatus`, `RetryPolicy` + jitter + `Retry-After`, `HTTPError` + `StatusOf`/`BodyOf`, `WithSleeper`. Zero call sites. | Unit tests: 503→200 succeeds in N attempts; `429 + Retry-After: 2` honored; non-transient 400 fails immediately; ctx cancel returns wrapped `context.Canceled` without extra attempts; `DoInto` decode error carries body; jitter bounds asserted. |
| **S2** — Auth + waits | `CachedToken`, `TokenSpec` (header + format), 401→invalidate→refetch→retry-once; `Ready`/`Wait`/`Stable`/`TolerateFailures` + predicates; `Ensure[T]`. | 401 refreshes exactly once then surfaces the error; `Stable(2)` doesn't return on oscillating body; `TolerateFailures` returns nil after a good read then timeout; `Ensure` performs **no** Apply when equal (asserted via recorder). |
| **S3** — Framework wiring | `Deps.HTTP` + `Deps.Assets`; shared transport built in `internal/appconfig.AppDeps`; zero-value `ClientFactory` usable. | `bloud configure prestart` (nil-deps path) still runs; a test asserts all clients from one factory share the transport pointer. |
| **S4** — Migrate the two cheapest apps | immich, affine (3 requests each, both already httptest-covered). | Existing httptest suites pass unchanged in intent; `./bloud validate --tier fast`; measured diff in the PR. |
| **S5** — `pkg/appasset` + `managedfile.Block` | Asset install (fetch/cache/verify/stage/commit, zip-slip guard), `Render`, `Block`/`RemoveBlock`, `ManifestVersionEq`/`ManifestFieldEq`. | Table test: sentinel skip makes zero network calls; checksum mismatch aborts with destination untouched; zip-slip entry rejected; commit is atomic (crash-simulated mid-unpack leaves no partial tree); `Block` handles missing/empty/unterminated regions and reports `changed` correctly. |
| **S6** — Migrate jellyfin + navidrome | jellyfin: split `api.go`, kill `DBG` logs, three retry loops → `Wait`, plugin installer → `appasset`. navidrome: dual-client (own API + Authentik with token header). | Existing 1178-line jellyfin suite passes against `appclient`; cold-start scenario from issue #71 re-verified via `./bloud e2e app BLOUD_E2E_APP=jellyfin`. |
| **S7** — Migrate homeassistant | The hardest: onboarding poll with `AlreadyDoneFunc` (404 + `ownerOnDisk`), trust probe, no-follow-redirects spec, asset install, `Block`. Keep `ownerOnDisk`/`storedProxyTrusted` in the app (disk facts, not HTTP). | 853-line HA suite passes with `WithSleeper(noop)`; stale-process force-recreate behavior (`PreStart:157-163`) preserved and asserted. |
| **S8** — Rebuild `pkg/authentik` on `appclient` | Replace 60 request blocks; add `ctx` propagation (today: `http.NewRequest`, no context → uncancellable); `Ensure*` semantics unchanged. | `client_test.go` + `client_email_test.go` + `client_scope_mapping_test.go` pass; `ensureSSO` install path verified in the integration tier. |
| **S9** — Kill the detach dance + fold `health.go` | Remove `context.Background()`/`WithoutCancel` from `PostStart`; framework-provided `PostStartBudget` (default 150s) wrapped around the call by the orchestrator; deletion of `WaitForHTTP`/`WaitForHTTPWithAuth`/`WaitForTCP`/`WaitForOpenIDConfig`/`ShouldWaitForSSO` (all zero-call-site); re-home `WaitForSSOReady` on `Wait`. | Regression test asserting a pass end does not cancel a running `PostStart`, and `Stop()` does; shutdown mid-`PostStart` is recorded as *interrupted*, not terminal `ERROR` (see §12 R3). |
| **S10** — Guardrail + docs | `scripts/no-adhoc-http.mjs` wired into `npm run test:precommit`: fails on `http.NewRequest` / `http.DefaultClient` / `.Do(req)` in `apps/**/*.go` (non-test); allowlist empty at end of S8. Rewrite `docs/guides/contributing-apps.md` Step 2 (which today documents a **stale interface** — `PreStart(…) error` and a `HealthCheck` hook that no longer exists); add "Verified constants" provenance checklist; add the client to `docs/architecture/overview.md`. | Pre-commit fails on a deliberately non-compliant file; guide example compiles against the real interface. |

Order rationale: build and prove the primitives before any app depends on them (S1–S3), take
the two cheapest wins to validate ergonomics (S4), build the asset layer before the app that
needs most of it (S5 before S6), leave HA (highest integration risk) until the primitives are
battle-tested, and land the guardrail last so it lands with an empty allowlist.

---

## 12. Risks

| # | Risk | Mitigation |
|---|---|---|
| R1 | Over-retry masks real failures; installs get slow instead of failing visibly | Every retry logs the attempt count; deadlines are explicit per call; `MaxAttempts` explicit for mutations; non-transient statuses fail immediately (D3/§5.3). |
| R2 | Retrying a non-idempotent `POST` double-creates resources | Verb retry policy (D3): mutations retry only with a declared outcome contract; the double-create window is bounded and logged. |
| R3 | Removing the detach makes shutdown cancel mid-`PostStart` → node lands in `ERROR` (terminal, §1.1) | In S9, cancellation-caused failures are recorded as interrupted: `ctx.Err()` in the error chain means "leave actual status where it is; re-converge on start" — the graph is in-memory anyway (`tech-debt.md:27-29`), so a restart re-converges from stores. |
| R4 | Touching the 2,872-line Authentik client risks the SSO critical path | S8 is mechanical and last-but-one; its three existing test files are the safety net; SSO provisioning is separately exercised in the integration tier (`./bloud validate --tier integration` installs Jellyfin through the real graph). |
| R5 | Abstraction leak: an app whose API doesn't fit the outcome contract | Escape hatches are first-class: `AlreadyDoneFunc`, `Ready` arbitrary predicate, `Body(...)` raw bodies, and a per-app `Client` override. Last resort: an app bypasses `appclient` for one call with a comment — the guardrail allowlist makes that visible rather than silent. |
| R6 | Framework accretes app knowledge over time | G6/D9 enforced in review: `pkg/appclient` and `pkg/appasset` must not contain app names. Guard: a test asserting those packages' source contains no app identifier strings. |
| R7 | Asset cache poisoned / disk growth | Digest-verified on every read from cache; mismatch deletes the entry. Cache capped (`MaxBytes`) with an LRU purge of `<data>/asset-cache` under a size limit (deferred; a few MB per asset today). |
| R8 | Migration churn delays product work | Each slice is one app or one package; nothing blocks the next feature. S1–S4 land value (jellyfin/HA duplication starts shrinking) before the risky S7/S8. |

---

## 13. Open questions

| # | Question | Leaning |
|---|---|---|
| Q1 | Jellyfin's comment claims `context.WithoutCancel` "did not prevent the cancellation on Go 1.25 linux/amd64" (`configurator.go:288-290`). Current code shows the pass context is process-scoped, so there should be nothing to prevent. Was the real cause a request-scoped context in an older install path? | Reproduce with a focused test in S9 before deleting; if it reproduces, fix the cause rather than keeping the workaround. Either way the outcome (framework-owned budget) removes the app's need to care. |
| Q2 | Should `PostStart` budgets move into `metadata.yaml` (`sso`/`installBudget`) instead of a framework default? | No for now — an orchestrator `PostStartTimeout` default with an override knob is enough; per-app budgets are only needed if real apps exceed 150s. |
| Q3 | Does the Authentik client stay host-owned (`pkg/authentik`) or become an "app client" like the others? | Keep host-owned — Bloud provisions the IdP itself and multiple subsystems (SSO provisioning, sharing, remote apps) use it. Only its *transport layer* changes (S8). |
| Q4 | Should cross-service calls (navidrome → Authentik) keep reading the token from disk (`navidrome/configurator.go:303`)? | Out of scope here, but the client makes the follow-up cheap: a `Secrets`-backed `TokenSource` would replace the file read with a managed source. Track as a follow-on to the durable-integration-state debt (`tech-debt.md:120-124`). |
| Q5 | Operator-visible call log? | Deferred. `WithRecorder` makes it a ~100-line follow-on (`GET /api/debug/configurator-calls`) once someone actually needs it in the field. |
| Q6 | Should `configurator.AppState` carry a per-call client so apps don't hold one? | No — the client is stateless per node with a stable base URL; holding it is cheaper and keeps call sites short. `BaseURLFn` covers host-set changes. |

---

## 14. Acceptance criteria (design-level)

1. `apps/**/*.go` (non-test) contains zero `http.NewRequest`, `http.DefaultClient`, and
   direct `.Do(req)` — enforced by pre-commit (S10).
2. Every configurator HTTP call declares its outcome; no ad-hoc `strings.Contains` on
   response bodies outside a declared `AlreadyDoneFunc` with a documented reason.
3. Exactly one download/verify/unpack implementation in the tree (`pkg/appasset`), used by
   jellyfin and homeassistant.
4. Exactly one wait implementation (`Call.Wait`); the five unused helpers in
   `pkg/configurator/health.go` are deleted or re-homed on it.
5. Every request in the tree carries a bounded timeout and honors `ctx`.
6. `PostStart` bodies contain no context detach.
7. Net reduction in app configurator code ≥ 40% (`apps/{jellyfin,homeassistant,immich,
   affine,navidrome}/configurator.go` is 3,125 LoC today: 1,066 + 1,082 + 303 + 311 + 363),
   with the existing configurator test suites (2,338 test LoC across the four app packages
   that have them) still passing and the new packages covered by their own unit tests.
8. `./bloud validate --tier fast` green per slice; `--tier integration` and
   `./bloud e2e lifecycle` green at the end of S9.

---

## Appendix A — Evidence index

| Claim | Location |
|---|---|
| ERROR is never retried | `internal/orchestrator/orchestrator.go:728-732` |
| PostStart error → `StatusError` | `orchestrator.go:981-985` (also PreStart `925-930`) |
| Pass context is process-scoped | `orchestrator.go:471-489` (`Start` → `converge(ctx, …)`; only `Stop` cancels) |
| Jellyfin detach + 3 retry loops + `DBG` logs | `apps/jellyfin/configurator.go:277-406` |
| HA detach (`WithoutCancel`) | `apps/homeassistant/configurator.go:199-203` |
| Per-call `http.Client` allocation | `apps/homeassistant/configurator.go:677,744,911,975` |
| Jellyfin auth header duplicated 7× | `apps/jellyfin/configurator.go:727,758,917,946,971,1001,1030` |
| String-matched idempotency | `apps/immich/configurator.go:261`, `apps/affine/configurator.go:198`, `apps/homeassistant/configurator.go:982` |
| Duplicate archive installers | `apps/jellyfin/configurator.go:110-214`, `apps/homeassistant/configurator.go:282-403` |
| Duplicate wait loops | `apps/immich/configurator.go:202-230`, `apps/affine/configurator.go:249-277` |
| Revert-resistant ensure (blueprint race) | `pkg/authentik/client.go:1324-1392` |
| Migration-lag retry | `pkg/authentik/client.go:1561-1620` |
| Dead helpers (zero call sites) | `pkg/configurator/health.go` — `WaitForHTTP`, `WaitForHTTPWithAuth`, `WaitForTCP`, `WaitForOpenIDConfig`, `ShouldWaitForSSO` |
| Authentik bypasses `Runtime.Exec` | `apps/authentik/configurator.go:23-40` vs `internal/container/runtime.go:64-68` |
| Existing correct primitive to extend | `pkg/managedfile/write.go` (atomic write + change detection) |
| Doc drift to fix | `docs/guides/contributing-apps.md:96-110` (`PreStart(…) error`, `HealthCheck` hook — neither exists) |
