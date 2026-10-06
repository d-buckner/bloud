# Pilot: hermes-webui as the first client-credentials app

> Status: implemented and shipped (PR on this branch). Work items A through J
> are done and green in CI. Two success criteria are closed as "not verified in
> the pilot" rather than passed, because neither can be exercised on the stock
> dev VM: SC1 (browser SSO) needs a real https deployment and SC2 (Hermex on a
> phone) needs a physical device. SC3 through SC7 are covered by tests or by
> the config assertions below.
>
> The pilot is scoped by two hard constraints:
>
> 1. **hermes-webui should use OIDC where possible** for browser sign-in.
> 2. **Hermex must be able to connect to hermes-webui.** Hermex takes a server
>    URL plus either a password or custom headers.
>
> Both are satisfiable at once, and the verified reason they do not conflict is
> the interesting part. All claims about both images were checked on 2026-10-06
> against `ghcr.io/nesquena/hermes-webui:0.52.113` and
> `docker.io/nousresearch/hermes-agent:v2026.9.14`.
>
> This supersedes an earlier draft of this pilot that scoped the credential to
> the `agentApi` bearer. That was the wrong credential for this goal: Hermex
> does not speak bearer to the webui, and the webui has no inbound bearer at
> all. The `agentApi` contract is still required, but as the webui's own
> backend connection. See "The agent connection".
>
> **Picking this up cold:** read "Current state and prerequisites" first, then
> "The agent connection", then the work items. The rationale sections in
> between explain why the design is what it is, and are worth reading before
> changing anything, but they are not where the executable detail lives.

## Current state and prerequisites

Read this before opening any file. Several things this plan depends on are not
where they look like they should be.

**hermes-webui is not in the catalog.** There is no `apps/hermes-webui/`
directory on `main` or on the current branch. Work item H creates it from
scratch. Do not go looking for it.

**Three stale branches hold reusable research.** None is mergeable as-is: all
three predate the `extraPorts` / `agentApi` work in #245, and two of them use
a retired `agentGateway` contract with a `httpToken` secret that no longer
exists. Mine them, do not merge them.

| Branch | Take | Do not take |
|---|---|---|
| `feat/app-hermes-webui-v2` | The container spec: image, env, healthcheck, and the first-boot venv-build budget (`retries: 180`). The `INTEGRATION.md` writeups of the model-key bug and the poison-marker trap | The `agentGateway` contract and its `httpToken` secret. The `requires: []` least-privilege note, which is wrong for the shape here |
| `feat/hermes-webui-mcp` | The uid-alignment finding (`WANTED_UID`/`WANTED_GID` must match across containers sharing a tree, and why uid 0 breaks the `su` re-exec). The `e2e/tests/hermes-webui.spec.ts` skeleton | The shared-`$HERMES_HOME` topology and the `ownsSharedData` / `shared_data.go` platform work. This pilot is gateway-backed |
| `feat/app-hermes-webui` | Nothing beyond what `-v2` has; it is the same work one revision back | Everything |

**The https prerequisite is hard and blocks SC1.** Native OIDC against this app
requires a real `https` origin whose hostname resolves to a genuinely public
address. The stock dev VM is plain http on `:8080` with `sso.localhost`, which
the app's own validator rejects on both the scheme and the address family.
There is no dev flag that makes SC1 pass on the stock VM. See "How to verify"
for what to do instead.

**What the platform already has**, so it is not part of this work: the
`extraPorts` primitive and the `agentApi` contract from #245, the
`AppSecretsProvider` store (`SetAppSecret`, `GetAppSecret`,
`GenerateAppAdminPassword`), the native-oidc client provisioning Bloud uses
for Immich and AFFiNE, and the container exec channel that reads and writes
files inside a running app.

## The two paths

| Client | Path | Mechanism |
|---|---|---|
| Browser | **native OIDC** through Authentik | `webui_oidc` config, Bloud-registered client |
| Hermex (iOS) | **minted password** | `HERMES_WEBUI_PASSWORD` → `POST /api/auth/login` → `hermes_session` cookie |

Constraint 1 is satisfied by the browser path. Constraint 2 by the Hermex
path. They coexist because the app treats its auth methods as a set rather
than a switch.

## Why they coexist: verified

`api/auth.py:538`:

```python
def is_auth_enabled() -> bool:
    """True if password auth, passkeys, OIDC login, or trusted-header auth is configured."""
    return (
        is_password_auth_enabled()
        or are_passkeys_enabled()
        or is_oidc_auth_enabled()
        or is_trusted_auth_enabled()
    )
```

An OR across all four mechanisms. Configuring OIDC does not disable password
login, and enabling the password does not alter the OIDC flow. That single
function is the whole reason both constraints can be met without one degrading
the other.

## Why the native OIDC path works here

The webui's OIDC client validates its own outbound URLs and is strict: it
requires `https` and rejects hosts resolving to local addresses
(`api/auth_oidc.py:_validate_outbound_oidc_url`). That is what makes native
OIDC impossible against `http://sso.localhost:8080` and against a LAN-issued
certificate.

It is satisfied by a real https public URL, which the deployment already
fronts Traefik with. Under that setup Bloud hands the app the public issuer
rather than the `sso.loopbackIssuer` override, the scheme check passes, and
the address-family check passes on a genuinely public resolution.

The callback path is readable straight off the app's public-path list:
`/api/auth/oidc/callback` is in `PUBLIC_PATHS` alongside
`/api/auth/oidc/start` (`api/auth.py:50-58`), so `sso.callbackPath` is
known rather than guessed.

## Why custom headers are the wrong lever

Hermex offers custom headers, and it looks like the elegant answer. It is the
dangerous one here, and the pilot does not use it.

The webui's trusted-header auth gates on `_raw_peer_is_trusted_proxy`
(`api/routes.py:5743`), judged on the raw socket address and documented as
"never a header, so it cannot be spoofed." Loopback is always trusted;
anything else must be named in `HERMES_WEBUI_TRUSTED_PROXY_CIDRS`.

Under Bloud the webui sits on `apps-net` and Traefik reaches it from there, so
the check fails closed today and trusted-header auth is inert. **The moment
Bloud adds that CIDR to turn it on, a client-supplied identity header passing
through the proxy becomes an asserted identity** unless the proxy strips the
client's version first.

The password solves the same problem without that coupling. Trusted-header
auth stays off in this pilot, and turning it on later is a separate design that
has to pair with header stripping at the TLS terminator.

## The agent connection

**This is a required part of the pilot, not an optional one.** SC2 cannot pass
without it. The credential work covers how a human reaches the webui; this is
how the webui reaches the agent, and the two are independent.

The pilot is **gateway-backed**: the webui dials Hermes' OpenAI-compatible
gateway over the `agentApi` contract rather than running the agent in-process
or sharing `$HERMES_HOME`.

```yaml
# apps/hermes-webui/metadata.yaml, alongside the blocks above
integrations:
  agentApi:
    required: true
    multi: false
    requires:
      - apiKey          # without this the binding carries no credential
    compatible:
      - app: hermes
        default: true
```

The configurator renders the binding onto three env vars, all confirmed to
exist in the image:

| Binding field | Env var | Read at |
|---|---|---|
| `AgentAPIBinding.Endpoint` | `HERMES_WEBUI_GATEWAY_BASE_URL` | `api/gateway_chat.py:42` |
| `AgentAPIBinding.APIKey` | `HERMES_WEBUI_GATEWAY_API_KEY` | `api/gateway_chat.py:43` |
| (constant) | `HERMES_WEBUI_CHAT_BACKEND=gateway` | `api/gateway_chat.py:41` |

The app's own error text states the contract: "Set `HERMES_WEBUI_GATEWAY_API_KEY`
to the same value as the Hermes Gateway `API_SERVER_KEY`"
(`api/gateway_chat.py:219`). Health resolution falls through
`GATEWAY_HEALTH_URL` > `HERMES_GATEWAY_HEALTH_URL` > `HERMES_API_URL` >
`HERMES_WEBUI_GATEWAY_BASE_URL` (`api/agent_health.py:490`), so setting the
base URL alone also satisfies the health probe.

The network side is already solved by #245 and needs no new work: the resolver
composes the routed `http://hermes.<host>:8080/v1` and
`applyRoutedProviderExtraHosts` in
`services/host-agent/internal/engine/orchestrator/execution.go` adds the
`host-gateway` pin so the name resolves from `apps-net`.

**Open question this pilot must answer:** does gateway-backed truly remove the
need for the agent source tree in the webui's venv? The `-v2` branch's notes
cite upstream's `docs/architecture/agent-api-contract.md` as saying the
source-tree share is still required and HTTP-only is the migration target. If
that is still true, the pilot needs `agent_source.go` and its first-boot build
budget regardless of the gateway, and SC2's latency profile changes
accordingly. Verify against the pinned image rather than trusting the note.

## The credential: what Bloud mints

This is the **provisioned** case rather than the relayed one, which is the
half of the pattern that has never been exercised.

```yaml
# apps/hermes-webui/metadata.yaml
sso:
  strategy: native-oidc
  callbackPath: /api/auth/oidc/callback
  userCreation: true

provides:
  clientPassword:
    secrets:
      - password
    clientAccess:
      reveal: once
      rotate: bloud
      label: "Mobile client password"
      reaches: "this web UI as one shared account, with the same access as any signed-in user"
      snippet: url-and-password
```

`clientPassword` is a new contract entry, which is what invariant 15 asks for:
a new capability gets a new contract rather than a new field on an existing
one. Note the semantic stretch it represents. `provides:` has so far meant
"provides to a consumer app through the graph". Here the consumer is a human
holding a phone. `clientAccess` is the field that makes that second audience
explicit rather than implicit.

### Password and session are independent: verified

This is the fact that makes a reveal-once credential workable, and it was not
obvious. The session cookie is **not** derived from the password.

`create_session` (`api/auth.py:579`):

```python
token = secrets.token_hex(32)
...
sig = hmac.new(_signing_key(), token.encode(), hashlib.sha256).hexdigest()
return f"{token}.{sig}"
```

`_signing_key()` reads a persisted 32-byte key from `STATE_DIR/.signing_key`,
generating and `chmod 0600`-ing it on first use (`_load_key`,
`api/auth.py:305`). The password is hashed with a *different* persisted key,
`STATE_DIR/.pbkdf2_key`, and only at login time.

Three consequences, all of them load-bearing for this design:

1. **Rotating the password does not, by itself, log anyone out.** It changes
   what a *new* login must present. Existing signed sessions keep verifying
   against the signing key and live out their TTL.
2. **Sessions survive a container recreate.** Both keys and the session store
   are files under `STATE_DIR`, which sits on the app's persistent volume.
   This closes the fragility question that would otherwise make SC2 a hollow
   pass.
3. **Rotation therefore has to clear the session store explicitly.** A stolen
   phone with a live session keeps working after a password rotation unless the
   session store is cleared in the same pass. The shipped behaviour does
   exactly that: rotate revokes.

### Lifecycle

Rotate is revoke-then-issue. It clears the session store, writes the new
password, and recreates the container once, so the app comes back with no live
sessions and only the new credential.

| Step | Effect |
|---|---|
| Mint | Bloud generates a long random password on install and stores it in `secrets.json` |
| Deliver | Written into the app config as `HERMES_WEBUI_PASSWORD` |
| Reveal | Shown once as a ready-made `{URL, password}` pair |
| **Rotate** | New password **and** live sessions killed, so the old credential is fully cut off |
| **Revoke** | Kill live sessions without changing the password |

The reveal-once model is workable because the recovery path is rotate. Lose the
password and rotate: you get a new value you can show, and every device signs
in again with it. That is the price of a one-time reveal, and it is the reason
the rotate confirm says it will log everyone out.

The reveal snippet is the natural mobile shape either way. Hermex needs a
server URL and a password, so the UI hands over both together rather than a
bare secret the user has to reassemble.

### Rotation revokes (shipped)

Rotation is revoke-then-issue. The API mints the new value, stores it, and
submits a revoke intent followed by a reconcile intent. The resync clears the
session store before `PreStart` renders the new config, so the recreate that
installs the new value comes back with no live sessions.

The reason it has to work this way is structural: the session layer has no
binding to the credential that created it. A session record carries `expiry`,
`auth_type`, `username`, and `bound_profile`, and `verify_session` checks the
signature, store membership, and expiry. Nothing in that path can be compared
against a rotated credential, because the session never recorded which
credential was in effect when it was minted. So there is no way to revoke a
rotated credential against only the sessions that used it; clearing the whole
store is the only mechanism, and a rotate that left sessions alive would be a
rotate that did not revoke.

This is tracked at the platform level too, in
[`client-credentials.md`](client-credentials.md). It is a property of the
pattern rather than of this app: a `rotate` that does not revoke is a weaker
primitive than the word implies.

## What this costs, honestly

The password is a single shared secret on a public https origin. Everyone who
holds it is the same principal in the webui's logs.

What mitigates it, verified:

- Bloud mints it long and random, so brute force is impractical.
- The app rate-limits login per IP and returns 429
  (`_check_login_rate`, `api/auth.py:272`).
- **There is no privilege tier.** No `is_admin`, no `is_superuser`, no role
  concept anywhere in the app. `auth_type` is consulted only in the logout
  flow, to decide whether to return a trusted-logout URL. The password
  account grants exactly what an OIDC session grants, and no more.
- Rotation is one click and does not disrupt a working device.

What does not mitigate it:

- **No per-user attribution.** Every Hermex user is the same account.
- **No per-device revocation.** The session store is global. Revoking logs out
  every live session, not the lost one, because there is no mapping from a
  device to a token that Bloud can act on selectively.
- **Rotation is not a security response.** Because it leaves sessions alone,
  rotating a suspected-compromised password does nothing about a device that
  is already in. The only response is revoke, and revoke is indiscriminate.
  **This is the gap the follow-up above closes**, and until it does, the UI
  should not let "Rotate" read like a way to lock someone out.

For a home deployment that is an acceptable trade, and the platform plan's
per-consumer token is the eventual answer. The UI has to state the trade
plainly at the reveal rather than let it read like a normal per-user
credential, and it has to say on the revoke control that it logs out
everyone.

## Success criteria

The pilot is done when all eight pass. SC2 is the acceptance test; the rest
are what makes SC2 safe to have shipped.

**SC1: Browser SSO works.** A browser at the public https URL reaches
hermes-webui, redirects to Authentik, signs in, and lands in the app. No
Bloud-minted password is typed anywhere in this flow.

**Cannot be exercised on the stock dev VM.** See "How to verify" for the two
acceptable ways to close this criterion. A plain-http `sso.localhost` run is
not one of them.

**SC2: Hermex connects to hermes-webui.** Manual test, on a real phone:

1. Open the `clientAccess` reveal on the hermes-webui tile.
2. Copy the server URL and the password into Hermex.
3. Hermex authenticates against the public URL without error.
4. A chat turn completes and returns a real model response.
5. The session survives an app background and relaunch.

Record the Hermex version and the webui image tag with the result. This is the
criterion the pilot exists to satisfy, and it is verified by hand rather than
automated, because the client is a phone.

**SC3: The reveal is one-time.** The password appears on the reveal action and
nowhere else. No `GET`, no home snapshot, no app payload, and no polled
endpoint returns the value. Asserted by a test that walks every response the
dashboard fetches and fails if the value appears in any of them.

**SC4: Rotation rotates and revokes.** After a rotate: the old password is
rejected at `/api/auth/login`; a fresh Hermex login with the new password
succeeds; and **the Hermex session established before the rotate stops
verifying and must re-authenticate.** This is what makes rotate mean "cut the
old credential off completely", so it is tested explicitly rather than assumed.

**SC4b: Revocation actually revokes.** After a revoke, the previously valid
Hermex session stops verifying and the app must re-authenticate, while the
password is unchanged. The control states on confirm that it logs out every
live session.

**SC5: No door was opened that was not declared.** Trusted-header auth stays
disabled: `HERMES_WEBUI_TRUSTED_AUTH_HEADER` unset,
`HERMES_WEBUI_TRUSTED_PROXY_CIDRS` unset. No `sso.bypassPaths` added. The
only non-OIDC entry point is the declared password.

**SC6: The UI is generated.** The reveal surface renders from the
`clientAccess` block with zero per-app UI code. Removing the block removes the
menu item. Asserted by rendering the same component against an app that
declares nothing.

**SC7: Coexistence holds.** With the password enabled, SC1 still passes
unchanged. The OIDC flow, the redirect URI set, and the session lifetime are
identical before and after provisioning.

## Work items

| Item | What it is | Files | Effort |
|---|---|---|---|
| **A.** `clientAccess` type in the contract registry | `reveal` (`never` / `once` / `always`), `rotate` (`bloud` / `provider` / `none`), `label`, `reaches`, `snippet`. Default `never` | `internal/catalog/types.go:37` (`ContractProvides`, add the field); `internal/catalog/contracts.go:20` (`Contract`, add the spec) |
| **B.** Loader validation | Reject `clientAccess` on a contract with no published secrets; require non-empty `reaches` when `reveal` is not `never`; fail a block naming a secret not in `secrets:` | `internal/catalog/loader.go`; new test beside `internal/catalog/agentapi_contract_test.go` |
| **C.** `clientPassword` contract | New contract entry, payload type, and the `url-and-password` snippet shape | `internal/catalog/contracts.go`; `pkg/configurator/interface.go` (binding type beside `AgentAPIBinding`) |
| **D.** Mint and deliver | Generate on install, store in `secrets.json`, write `HERMES_WEBUI_PASSWORD` in `PreStart`, idempotent under the resync | `apps/hermes-webui/configurator.go`; `internal/secrets/manager.go:344` is the store, unchanged |
| **E.** Reveal endpoint | `POST` returns value plus snippet and writes an audit record; `GET` returns metadata only. Plus the SC3 non-pollable test | New `internal/api/settings_client_credentials.go` beside `settings_public_url.go`; mount in `registerRoutes` at `internal/api/router.go:342` |
| **F.** Rotate | Regenerate, rewrite, re-publish, and revoke the session store (SC4) | Same module as E; `apps/hermes-webui/configurator.go` for the rewrite |
| **G.** Revoke | Clear the app's persisted session records through the container exec channel, plus the SC4b test and the "logs out everyone" confirm copy | Same module as E; exec channel via `pkg/configurator` `Exec` dep |
| **H.** hermes-webui as a native-oidc app | OIDC client registration, `callbackPath`, the agent connection above, the container spec, and the app's own configurator | New `apps/hermes-webui/{metadata.yaml,configurator.go,registration.go,icon.png}`; register in `apps/registry.go`; add to `validation.yaml` |
| **I.** UI | Reveal / rotate / revoke surface on the app detail modal, generated from `clientAccess` with zero per-app code | `web/src/lib/components/AppDetailModal.svelte`; new `ClientAccessPanel.svelte` |
| **J.** Docs | `features/` entry for client credentials, and the pilot result written back | `docs/features/client-credentials.md`; update this doc's status |

Roughly seven to eight days. A through G are the platform pattern, H and I are
the app, J closes it.

## How to verify

Per-item checks, from the repo root.

```bash
# A, B, C: catalog shape and validation
cd services/host-agent && go test ./internal/catalog/... -run 'Contract|ClientAccess' -v
npm run lint:go && npm run check:gofmt

# D: the credential is minted, stored, and idempotent across a resync
cd apps && go test ./hermes-webui/... -v

# E, F, G: reveal / rotate / revoke behaviour
cd services/host-agent && go test ./internal/api/... -run 'ClientCredential' -v

# SC3 specifically: the value appears in no polled response
cd services/host-agent && go test ./internal/api/... -run 'NonPollable' -v

# H: the app loads, plans, and registers
cd apps && go test ./configtest/... && go test ./registry_test.go -v
./bloud depgraph --check && ./bloud catalogdoc --check

# Whole-tree gates before any commit
./bloud validate --tier fast
```

**Running the app.** `./bloud dev` brings up the stack; the dashboard is on
`http://localhost:8080` and the host-agent API on `:3000`. Install with
`./bloud install hermes-webui` after `hermes` is up, since the agent
connection orders them.

**SC1 cannot be exercised on the stock dev VM.** Native OIDC needs a real
https origin with a hostname that resolves to a public address; the VM serves plain http on
`:8080` with `sso.localhost`, which the app rejects on both the scheme and
the address family. Two options, and the choice should be recorded with the
result rather than left implicit:

1. Test SC1 against a real https deployment behind the operator's TLS proxy,
   which is the only environment where the criterion means what it says.
2. Mark SC1 as **not verified in the pilot** and proceed on the strength of
   the code reading in "Why the native OIDC path works here", with the
   explicit note that the criterion is unproven.

Do not mark SC1 passed on the dev VM. It cannot pass there, and a false pass
on the browser SSO criterion is worse than an honest gap.

**SC2 is manual and stays manual.** It needs a phone. Record: the Hermex build
or version, the webui image tag, the instance's public URL, the time, and
whether the session survived a background and relaunch. The phone must be able
to reach the instance's public https origin, which on a home network usually
means the same LAN or a tunnel; a phone on cellular hitting a LAN-only host is
not a pilot failure and should not be recorded as one.

**SC5 is a config assertion, not a test.** Grep the generated Traefik config
and the app's env for `HERMES_WEBUI_TRUSTED_AUTH_HEADER`,
`HERMES_WEBUI_TRUSTED_PROXY_CIDRS`, and any `bypassPaths`, and confirm all
absent.

## Out of scope, tracked in the platform plan

| Item | Why it waits |
|---|---|
| **Revoke on rotate** | Shipped: rotate submits a revoke intent before the reconcile, so the recreate installs the new value with an emptied session store |
| Trusted-header per-user identity | Requires header stripping at the TLS terminator to be safe. The password already solves Hermex |
| Per-consumer tokens | The answer to "revoke one device"; needs its own design |
| Persistent reveal (`reveal: always`) | Gated on the forgeable auth-bypass repayment |
| The Hermes dashboard `basic` provider | Hermex targets the webui, not the dashboard, so that door stays closed |
| Passkeys for Hermex | The mobile client does not offer a passkey flow |

## Open questions

1. **Is the webui's OIDC client public or confidential?** Bloud defaults to
   confidential and offers `sso.clientType: public` for PKCE. Which one the
   webui expects determines the Bloud-side client registration, and it is
   determined by reading `webui_oidc` config handling rather than guessed.
2. **Does the password land in the app's config or in env?** Env is visible in
   `podman inspect`; the config file is not. The config path is preferred if
   the app reads it there, which needs confirming.
3. **Can Bloud clear the webui's session store cleanly?** Revocation depends
   on it. The store is a file under `STATE_DIR` owned by the app's runtime
   uid, so the write goes through the container exec channel. Whether that can
   remove specific tokens rather than the whole file decides if per-device
   revocation is reachable later without a redesign.
4. **Does the reveal need to name who it was shown to?** With one shared
   account there is no per-user audit, so recording the reveal event is the
   only attribution available.
