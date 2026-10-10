> Status: draft

# Plan: sign-in exchange: what a remote app asks for when it has no pasteable key

## Problem

`docs/plans/external-apps.md` made the remote-app form generated rather than
authored: the contract registry already states what a provider must publish, so
the form asks for the endpoint plus one field per secret the app's `provides:`
declares. For a remote Sonarr or Radarr that is exactly right: the key exists,
it is visible in that app's own settings page, and pasting it is the shortest
path from "I already run this" to "Bloud wires it".

Seerr is not like that. Its `provides:` declares `requestManager: secrets:
[apiKey]`, so the form asks for **Api Key**, and the help text says "read it
from that instance". But the credential a person actually holds when they set
Seerr up is a **sign-in**: Seerr has no local password by default, its admin is
the Jellyfin account that completed the first-run wizard
(`apps/seerr/INTEGRATION.md`), and the API key is a string sitting in a
settings box most people have never opened. The form asks for the one thing the
operator is least likely to have to hand, for the one app whose whole purpose is
being the front door of the media stack.

For a Seerr Bloud boots this is already solved: the configurator reads
`main.apiKey` out of `settings.json` after boot (`apps/seerr/configurator.go:
readAPIKey`) and publishes it, and nobody types anything. The remote case has no
configurator and no filesystem, so the same answer has to come through the
network instead.

## Decision

**An app can declare that its remote credential is exchanged, not pasted.** The
operator supplies a sign-in; Bloud logs in to the remote instance with it, reads
the credential the contract needs out of the app's own API, and stores *that*.
The contract, the resolver, and every consumer are untouched: `arr-mcp` still
receives `requestManager.APIKey`, and it has no way to tell whether a human
pasted it or Bloud traded a login for it. That is the same load-bearing claim
external apps were built on ("a consumer configurator should need zero
changes"), applied to the operator's side of the boundary instead of the
consumer's.

Two consequences worth stating before the design:

- **The exchange happens at save time, in the API boundary, not in a
  reconciliation.** The resolver must stay a pure read of stored state
  (invariant 15's anti-probe rule is about exactly this), and an external record
  has no node, so there is no pass to hang it on. The credential is therefore
  as fresh as the moment it was saved: if the operator regenerates Seerr's API
  key later, the stored one goes stale and the fix is to re-save the record.
  That is precisely the failure mode of a pasted key today, so the exchange is
  strictly better: it cannot be wrong at the moment of saving, and it asks for
  a credential the operator already has.
- **The sign-in Bloud asks for is the one Bloud already holds when it can.**
  When Jellyfin is installed here, its `mediaServer` contract publishes the
  bootstrap admin login, which is the account a Bloud-shaped Seerr was
  onboarded with. So the form offers "use the Jellyfin admin login Bloud
  already has" and the password never crosses the browser at all. The typed
  inputs are the fallback for a media server Bloud does not run, or a Seerr
  whose admin is a different account.

## What the code already knows

Nothing here needs a new paradigm; it needs one small interface and one place to
call it.

- **The form is already derived.** `providerContractFields`
  (`internal/api/external_apps_provider.go`) builds fields from
  `declaredValueKeys` plus `spec.Secrets`, and the dashboard renders whatever
  arrives (`web/src/lib/utils/providerInputs.ts`). A field the backend stops
  emitting is a field the form stops asking for, with no per-app code in the
  frontend.
- **Cookie-session sign-in from the host is a solved shape in this repo.**
  `apps/affine/api.go` and `apps/paperless-ngx/api.go` both build a second
  `appclient.Client` with a `cookiejar` because the credential lives in a
  session cookie rather than a header. The Seerr exchange needs the same thing
  and can copy the pattern.
- **The login is already resolvable.** `AppSecretsProvider` carries
  `GetAppSecret` and `GetAppContractValue`, which is how
  `orchestrator/integrations.go` builds a `mediaServer` binding today. The API
  module already holds the same provider (`m.secrets`) and already reads it for
  the "credential is set" indicator (`toResponse`).
- **The app module already self-registers into package-level registries**
  (`apps/<name>/registration.go` → `configurator.MustRegisterFactory`), so
  Seerr-specific wire knowledge stays in `apps/seerr/` where
  `api.go` and `INTEGRATION.md` keep it, instead of leaking into the generic
  external-apps module.

## The mechanism

One interface, in `pkg/configurator` (stdlib + `appclient` only, per invariant
14), registered by catalog app id:

```go
// CredentialExchange turns operator-supplied inputs into the credential a
// contract needs, by asking the remote app itself.
type CredentialExchange interface {
    // Contract is the contract whose secret the exchange replaces.
    Contract() string
    // Inputs are the fields the operator fills when Bloud holds no login.
    Inputs() []ExchangeInput
    // LoginContract names a contract whose provider publishes a login Bloud
    // may use instead of typed inputs ("" when there is no such shortcut).
    LoginContract() string
    // Exchange performs the trade and returns the contract payload.
    Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
}
```

`ExchangeRequest` carries everything host-shaped, so the exchange itself is a
stateless protocol adapter and the API needs no `Deps` to build one:

| Field | Contents |
| --- | --- |
| `Endpoint` | the remote origin the record points at |
| `Inputs` | what the operator typed, keyed by `ExchangeInput.Key` |
| `Login` | the login Bloud resolved from `LoginContract`, nil when it has none |
| `HTTP` | the process `ClientFactory`, for building the remote client |
| `Logger` | the host logger |

`ExchangeResult` returns `Secrets` and `Values` **keyed by contract**, the same
shape `ExternalAppSpec` stores, so an exchange that fills two contracts can do
it in one trade.

The registry mirrors the configurator factory registry:
`RegisterCredentialExchange(catalogID, exchange)` from the app's `init()`,
`LookupCredentialExchange(catalogID)` for the two callers.

### The login-shaped contract

`LoginContract` returns `"mediaServer"`, and the API resolves it generically:
a contract whose registry entry declares exactly one secret and exactly one
declared value *is* a login (the secret is the password, the value is the
username), which is precisely how `mediaServer` documents itself
("the username is a value and the password is a secret, because that is what
they are"). Anything else is not login-shaped and resolves to none. The
resolution reads the installed provider that offers the contract:
`GetAppSecret(app, <secret>)` for the password, `GetAppContractValue` then the
static declared value for the username. No app name appears in the code.

This is a platform read, not an app read: invariant 15 gates what a *consumer's
binding* may contain, and the exchange is Bloud's own machinery acting on
credentials Bloud minted and stores. The exchange receives the login as an
argument; it never reaches into the secrets manager.

### The form

`GET /api/external-apps/providers` gains one block per option, present only
when the app registered an exchange:

```json
{
  "app": "seerr",
  "contracts": [{ "name": "requestManager", "fields": [] }],
  "exchange": {
    "contract": "requestManager",
    "inputs": [
      { "key": "username", "label": "Username", "kind": "value", "required": true },
      { "key": "password", "label": "Password", "kind": "secret", "required": true }
    ],
    "storedLogin": "the Jellyfin admin login Bloud already has"
  }
}
```

Two things change on the server side and one on the client:

- the exchanged contract's own secret fields are **not emitted** (the exchange
  replaces them; asking for both would let the operator paste a key Bloud then
  overwrites),
- `storedLogin` is a label, present only when the resolution above found a
  login, so the form can offer the checkbox instead of two inputs,
- the add/update body gains `exchange` (the typed inputs) and `exchangeLogin`
  (use the stored one). An update that names neither means "leave the credential
  on file alone", which is the existing rule for secrets and stays intact.

## The Seerr exchange

`apps/seerr/exchange.go`, with its calls added to `api.go` and cited there like
every other Seerr endpoint Bloud touches. Verified against the pinned
`v3.4.1` source:

| Step | Call | Why it works |
| --- | --- | --- |
| 1 | `GET /api/v1/settings/public` | `initialized: false` means the instance has never been set up. Refuse with "complete its own setup first": pointing it at a media server is the wizard's job, not this form's. |
| 2 | `POST /api/v1/auth/jellyfin` `{username, password}` | `server/routes/auth.ts`: on a configured instance the route logs the credentials into **Seerr's own** Jellyfin and sets `req.session.userId`. `seerr-api.yml` requires only `username` and `password`, and `hostname` must be **omitted** once `settings.jellyfin.ip` is set, or the route answers 500 "Jellyfin hostname already configured". |
| 3 | `POST /api/v1/auth/local` `{email, password}` | The fallback for a Seerr whose admin is a local account (`settings.main.localLogin`, on by default). Tried only when step 2 is refused, so one username/password box covers both shapes. |
| 4 | `GET /api/v1/settings/main` (session cookie) | `server/routes/settings/index.ts` returns `main` with `apiKey` for an admin and `omit(main, 'apiKey')` for anyone else. An empty `apiKey` in the response is therefore a definitive "this account is not an admin in Seerr", which is the message worth showing. |
| 5 | publish | `secrets[requestManager] = apiKey`, `values[requestManager] = {defaultUser: <the account signed in>}`. `defaultUser` is what `arr-mcp` writes as `default_user`, the identity requests are attributed to, and the account we just signed in as is the honest answer for a remote install. |

Notes on the details that could bite:

- **The password goes to the Seerr origin, not to a Jellyfin the operator
  names.** Step 2 hands the credentials to the Seerr the record points at, and
  Seerr dials the Jellyfin *it* already has configured. The endpoint is
  operator-supplied and the route is on the admin surface, so this is not a new
  secret path, but it is a new place a stored admin password is transmitted:
  under a plain-http origin it crosses the LAN in clear. The form says so when
  the origin is http.
- **CSRF.** `settings.network.csrfProtection` defaults to `false`
  (`server/lib/settings/index.ts`), which is why the existing configurator can
  POST unauthenticated routes with no token. When an operator has turned it on,
  Seerr issues the token as the `XSRF-TOKEN` cookie, so the session client
  echoes it back as `X-CSRF-TOKEN` when the jar holds one. Cheap, and it turns
  a confusing 403 into a working exchange.
- **The exchange writes no settings.** A sign-in is not literally free of side
  effects: `POST /auth/jellyfin` creates a Seerr account for a Jellyfin user that
  has none, which is what signing in means and is not something Bloud can avoid.
  What it must never do is rotate. Calling `POST /settings/main/regenerate` would
  replace a credential the operator's other integrations may already be using.

## What does not change

- **The contract registry.** `requestManager` still declares `apiKey`. The
  exchange is about how the operator *supplies* it, not about what the contract
  is, and changing the contract would break the local provider that publishes it
  and every consumer that reads it.
- **Consumers.** `arr-mcp` reads `binding.APIKey` and `binding.DefaultUser`.
- **The resolver, the graph, the store schema.** The record stores the same
  per-contract secret it stores today; only who produced it differs.
- **Every other remote app.** An app with no registered exchange keeps the
  generated secret fields exactly as they are. Radarr, Sonarr, Jellyfin, AFFiNE:
  untouched.

## Tests

- `apps/seerr/exchange_test.go`: a fake Seerr (httptest) asserting each refusal
  and the success path: uninitialized, Jellyfin-login-only, local-login-only,
  non-admin (no `apiKey` in the response), unreachable. Plus the CSRF echo.
- `internal/api/external_apps_provider_test.go`: the field derivation stops
  emitting `apiKey` and emits the exchange block; `storedLogin` appears only
  when a local media server published one; the add path stores the exchanged key
  and never the typed password; an update naming neither keeps the stored key.
- `web`: `providerInputs` vitest for the exchange payload and the submit gate.
- `apps/conformance_test.go` keeps its rule that a registered exchange names a
  real contract of the app that registered it, so an exchange cannot quietly
  point at a contract the app stopped providing (the same "an exemption that
  guards nothing is a failure" shape as `operatorValues` and the image-pin
  exceptions).

## Open questions

1. **Re-exchange on drift.** If a consumer starts getting 401s from a remote
   Seerr, Bloud could offer "re-run the sign-in" rather than a full re-save.
   That needs a place to run it (an explicit API action, not a reconcile pass)
   and a reason to believe the login is still valid. Deferred until someone
   hits it.
2. **Which apps next.** The same shape fits any app whose remote credential is
   a login. Nothing else in the catalog needs it today: the Servarrs publish
   pasteable keys, Jellyfin publishes its admin login directly, AFFiNE's
   `appApi` password is already the typed credential.
3. **Does `defaultUser` belong to the exchange at all.** It is currently the
   signed-in account's own name. If a remote Seerr's admin is an LDAP/Jellyfin
   account and requests should be attributed elsewhere, the value needs its own
   input; the exchange returning it is the conservative default, not a
   provenance claim.
