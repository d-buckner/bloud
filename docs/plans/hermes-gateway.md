> Status: goal 1 implemented in this branch (the `offline_access` + 90 day
> lifetime fix, see "Goal 1" below). Goal 2 is still research. Verified
> against the pinned image `docker.io/nousresearch/hermes-agent:v2026.9.14`
> (code_version 0.21.3), upstream issues, the Hermex client source, and
> Bloud's own Authentik blueprint templates, 2026-10-05.

# Research: Hermes for native clients (Desktop app, Hermex)

## The two goals

1. Stop the repeated login prompts the Hermes macOS Desktop app hits against a
   Bloud-hosted Hermes.
2. Let third-party clients like Hermex connect to the Bloud-hosted agent.

Neither goal is about the Telegram/Discord messaging gateway. Goal 1 has a
specific, Bloud-side root cause with a small fix. Goal 2 needs a credential
path Bloud does not currently create.

## Goal 1: the Desktop login loop

**Root cause: Bloud never asks Authentik for a refresh token, and Bloud sets
the access token lifetime to five minutes.**

The chain, end to end, all of it in this repo:

1. `apps/hermes/configurator.go:40` hardcodes the scope set written into
   Hermes' self-hosted OIDC config:

   ```go
   // managedScopes is the OIDC scope set written into the self-hosted provider.
   managedScopes = "openid profile email"
   ```

   No `offline_access`. Hermes therefore never requests a refresh token.

2. Bloud's native-OIDC provider creation
   (`services/host-agent/pkg/authentik/nativeoidc.go`) sets the lifetimes:

   ```go
   const defaultAccessTokenValidity = "minutes=5"   // line 16
   ...
   "access_token_validity":  tuning.accessTokenValidity(),  // line 171
   "refresh_token_validity": "days=30",                     // line 172
   ```

   The `oidcBlueprintTemplate` in `internal/sso/blueprint_templates.go:41-43`
   carries the same three values. Five minutes is the number the user sees.

3. Hermes' dashboard session lifetime tracks the IdP access token's `exp`
   (`hermes_cli/dashboard_auth/request_utils.py:access_token_max_age`). The
   session cookie's `Max-Age` follows the token TTL.

4. When the access token expires, the middleware's refresh path needs a
   refresh token to rotate with. `middleware.py:_attempt_refresh` starts with
   `if not refresh_token: return None`, and the caller turns that into
   `_session_expired_response`, which audits
   `session_verify_failure reason=no_provider_recognises` and falls back to
   `login_start reason=auto_sso`.

5. In a browser the auto-SSO bounce is a redirect the user barely notices. In
   the macOS Desktop app it surfaces as a login prompt. Every five minutes.

This is not speculation about our stack. Upstream issue
[#90000 "Self-hosted OIDC session invalid after access token expiry with
Authentik"](https://github.com/NousResearch/hermes-agent/issues/90000) reports
the identical signature against Authentik with a five-minute access token:

> ```
> {"event":"session_verify_failure","reason":"no_provider_recognises"}
> {"event":"login_start","provider":"self-hosted","reason":"auto_sso"}
> ```
> The failure occurs exactly 5 minutes after login, matching the Authentik
> access token lifetime. [...] Increasing the Authentik Access Token Lifetime
> from 5 minutes to 60 minutes delays the issue significantly.

Open, `P3`, `needs-repro`. The missing refresh scope is the recognized shape
of this bug upstream: [#83294, request offline access for Google
OIDC](https://github.com/NousResearch/hermes-agent/issues/83294) and
[#100147, self_hosted OIDC provider never receives a refresh_token from
Google](https://github.com/NousResearch/hermes-agent/issues/100147).

Note that the user's TLS setup is not the problem and was never the fix. Under
a https public URL Bloud skips the `sso.loopbackIssuer` override and hands
Hermes the public issuer, which is why login works at all from a remote Mac.
The loop is what happens five minutes *after* a successful login.

### The fix

Both halves are required: Authentik must be willing to issue a refresh token,
and Hermes must ask for one.

**Half 1, the Authentik side: already plumbed.** `sso.scopes` is an existing
per-app field for `native-oidc` apps. It flows
`app.SSO.Scopes` → `OIDCInputs.ExtraScopes` → `authentik.OIDCTuning.ExtraScopes`,
and the type's own doc comment names this exact use case:

```go
// ExtraScopes are scope names added to the provider on top of openid,
// profile and email (e.g. "offline_access").
ExtraScopes []string
```

So `apps/hermes/metadata.yaml` gains:

```yaml
sso:
  strategy: native-oidc
  scopes:
    - offline_access
```

**Half 2, the Hermes side: a one-line change in the configurator.**
`managedScopes` has to include `offline_access` so the scope reaches the
authorize request. The cleanest shape is to derive the written scope set from
the app's declared `sso.scopes` rather than a constant, so the two halves
cannot drift: one declaration in `metadata.yaml` drives both the Authentik
provider mapping and the scopes Hermes requests.

**Optional third knob: widen the session.** `sso.accessTokenMinutes` is also
already plumbed through `OIDCTuning.accessTokenValidity()`. Raising it (60,
say) reduces the blast radius of any remaining refresh problem and costs
nothing structurally. Vaultwarden already uses both fields, per
`apps/vaultwarden/INTEGRATION.md`.

**Tests.** A unit test that the generated `config.yaml` scope string includes
`offline_access`; a blueprint test that the Authentik provider carries the
matching scope mapping; and a regression test that a dashboard session survives
past the access-token TTL via the refresh grant rather than falling back to
`auto_sso`. That last one is the test that actually matters, and it is the one
upstream does not have.

**Verification needed against the live install before trusting any of it.**
Two checks, both quick:

1. Sign in to Hermes through Bloud's SSO and inspect the token Authentik
   returns. If there is no `refresh_token` in the response body, the scope is
   not being granted and half 1 is not done.
2. With the change in place, watch the dashboard-auth audit log past the old
   five-minute boundary. `refresh_success` is the pass condition.
   `session_verify_failure reason=no_provider_recognises` means the refresh
   token exists and Hermes still cannot rotate with it, which is upstream
   #90000 in its unfixed form.

**Estimate: about a day** for the change plus tests. Add a verification pass
against the real install. If the refresh path turns out broken upstream
regardless of the scope, the fallback is widening `accessTokenMinutes` and
tracking #90000, which is still a day but is a mitigation rather than a fix.

## Goal 2: third-party clients (Hermex)

Hermex (`uzairansaruzi/hermex`, "Native iPhone app for your Hermes agent")
has two server modes. Its primary mode targets `nesquena/hermes-webui`, a
different third-party project Bloud does not ship. The relevant mode is
**Bot Mode**, which connects directly to the official `hermes-agent`.

Its contract, from `docs/agents/bots.md` and
`HermesMobile/Networking/Hermes/HermesConnection.swift`:

1. `GET /api/status` (public). A JSON object carrying `auth_required` is
   detected as a Hermes dashboard. It also reads `install_id` as the host's
   stable identity, trust-on-first-use, re-checked on every later sign-in.
2. **Username and password sign-in.** `HermesConnection.signIn()` posts
   `username` and `password`. This is the dashboard's `basic` provider.
3. `POST /api/auth/ws-ticket` for a single-use 30-second ticket, then the
   WebSocket upgrade presenting it.
4. Version gate: Hermex **refuses any host older than Hermes 0.21.3**, "the
   first release with the gateway contract it is built on". Bloud's pin
   reports `code_version: 0.21.3`, exactly at the floor. That is fortunate and
   fragile: the pinning checklist in `INTEGRATION.md` should name
   `/api/status`, the ticket endpoint, and the WS frame shapes as things to
   re-verify on every bump.

**The gap: Bloud registers no `basic` provider.** The Hermes app registers only
the self-hosted OIDC provider, whose login is a browser redirect. Hermex posts
a credential form and cannot drive that redirect. Against a Bloud-hosted
Hermes today, Hermex can detect the dashboard and cannot sign in.

Closing it means enabling the dashboard's bundled username/password provider:

```
HERMES_DASHBOARD_BASIC_AUTH_USERNAME=<generated>
HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=<generated>
HERMES_DASHBOARD_BASIC_AUTH_SECRET=<32+ random bytes, stable across restarts>
```

All three belong in Bloud's secrets provider (`AppSecretsProvider`), never a
template literal, per invariant 8. The `_SECRET` in particular signs sessions:
a value regenerated per boot invalidates every client session on every
container recreate, which is the login loop again by a different route.

Bloud then has to get the password to the user. There is no per-app credential
display in the dashboard today, so the minimum is a one-time reveal path and
the honest version is a Bloud UI surface for app client credentials.

**This is a security decision, not plumbing.** A generated password is a
credential path Bloud's SSO does not govern. It sits beside Authentik rather
than behind it: the dashboard renders a provider chooser when more than one
provider registers, so the basic path is available to anyone who finds it, and
it is a static shared secret on a box running an agent with terminal access.
Invariant 6 says the strategy set is exactly `native-oidc`, `ldap`,
`forward-auth`, `none`. This adds no Bloud strategy; it adds a second,
Bloud-minted door inside an app Bloud already authenticates through Authentik.
That deserves an explicit call before anything gets built.

**Estimate: 2 to 4 days** for the provider, the secret lifecycle, docs, and
tests, plus the decision, plus whatever the credential reveal costs.

### The other third-party path: the OpenAI-compatible API server

**Shipped.** This is the path Bloud took, and the diagnosis below was wrong in
a way worth recording, because it pointed at the wrong layer.

The gateway exposes an OpenAI-compatible endpoint on port 8642 that any
OpenAI-format frontend can drive: a scoped bearer token, no login page. It is
offered under a new `agentApi` contract rather than folded into `inference`, on
a newly declared `extraPorts` surface, routed through Traefik. See
[`docs/features/agent-api.md`](../features/agent-api.md) for the platform
shape and `apps/hermes/INTEGRATION.md` for the app-specific facts.

**The blocker named here was not the blocker.** App-to-app traffic never touches
Traefik, so "one backend per app" was not what stopped a consumer from dialing
the agent. The real constraint was a network-namespace boundary: Hermes runs
`network: host` for its loopback OIDC issuer, and a host-networked container is
not resolvable by container name from `apps-net`. Verified against the live
podman environment:

| Test, from an `apps-net` container | Result |
|---|---|
| resolve `apps-traefik` (host-net) | does not resolve |
| reach a host-net service bound `0.0.0.0` via `host.containers.internal` | reachable |
| reach a host-net service bound `127.0.0.1` | not reachable |
| bind the `apps-net` gateway from a host-net container | fails, not an address in that namespace |
| attach one container to both `host` and a bridge | rejected by podman |
| override `localhost` with `--add-host` from a bridge container | silently ignored |

That last row is why the loopback issuer cannot be rescued on a bridge network,
and it is what made routing through the proxy the only shape that keeps Hermes
where it is.

Two corrections to the text above, both verified against the pinned image:

- There is **no `API_SERVER_ENABLED` flag**. The adapter starts when
  `API_SERVER_KEY` is usable, meaning at least 16 characters
  (`gateway/config.py::_has_usable_api_server_key`). A short key is not a weak
  credential, it is a missing one.
- The gateway is not an s6 service. It runs as the container's **main program**
  (`command: ["gateway", "run"]`), not via `HERMES_GATEWAY_BOOTSTRAP_STATE`
  alongside a parked `sleep`. Keeping dashboard and gateway in one container is
  deliberate: the dashboard's liveness check needs a shared PID namespace, and
  splitting them trips upstream #73796.

## The messaging gateway (worth doing, unrelated to both goals)

The gateway process is what fires cron jobs. `hermes_cli/cron.py` warns that
with no gateway running, jobs won't fire automatically. Scheduled automations
are in the Hermes app's own `description` in `metadata.yaml`. If Bloud wants
that claim to be true, the gateway has to run.

Verified on the pinned image: adding `HERMES_GATEWAY_BOOTSTRAP_STATE: "running"`
to the container environment makes the image's own boot reconciler
(`/etc/cont-init.d/02-reconcile-profiles`) register and start
`/run/service/gateway-default` running `hermes gateway run --replace`,
alongside the dashboard, with Bloud's existing
`command: ["sleep", "infinity"]` unchanged.

```
/run/service/dashboard:        up (pid 116)
/run/service/gateway-default:  up (pid 135)
135 hermes  /opt/hermes/.venv/bin/hermes gateway run --replace
```

Three properties checked while in there:

- **No new listener.** The only bound socket was `127.0.0.1:9119`. The gateway
  binds nothing until a port-binding platform or the optional API server is
  configured, so under `network: host` there is no new host surface.
- **It runs with zero platforms configured.** `No messaging platforms enabled`
  then `Gateway will continue running for cron job execution`. No bot token
  needed.
- **Restarts hold.** A `podman restart` came back `prior_state=running
  action=started`, because the gateway persists `running` on signal teardown
  (upstream #42675). Bloud's recreate-on-config-change does not strand it.
  The seed is first-boot-only by design, so a gateway the operator stops inside
  Hermes stays stopped.

Cost: ~190 MB RSS idle on top of the dashboard's ~160 MB. Roughly double this
app's idle memory.

**Estimate: half a day**, most of it rewriting the `metadata.yaml` comment
that currently says "no gateway required".

## Effort summary

| Work item | Unblocks | Effort | Blocked by |
|---|---|---|---|
| `offline_access` in `managedScopes` + `sso.scopes` in metadata | Goal 1 | ~1 day | live verification |
| `sso.accessTokenMinutes` for Hermes | Goal 1 mitigation | included | nothing |
| `basic` provider + secret lifecycle + credential reveal | Goal 2 (Hermex) | 2 to 4 days | a security decision |
| Multi-port route generation in `traefikgen` | generic OpenAI clients | 3 to 5 days | nothing (platform work) |
| Gateway API server + key | generic OpenAI clients | ~1 day | the route work |
| `HERMES_GATEWAY_BOOTSTRAP_STATE=running` | cron / automations | 0.5 day | nothing |

## Recommended order

1. **Goal 1 now.** The root cause is identified, the plumbing already exists,
   and the change is a scope string. Verify against the live install first to
   confirm Authentik grants `offline_access` for this public client.
2. **Gateway enablement now.** Half a day, makes the app's own description
   true, adds no listener.
3. **Goal 2 after a decision.** The Hermex path is cheap but opens a non-SSO
   credential door. Get the call before the code.
4. **Multi-port routing as platform work regardless.** It is the shape that
   makes third-party integration a supported thing rather than a per-app hack.

## Open questions

1. **Does Authentik grant `offline_access` to a public PKCE client in
   Bloud's configuration?** If not, goal 1 needs a provider-side change
   beyond the scope list, and the mitigation is `accessTokenMinutes`.
2. **Is upstream #90000 fixed by the scope, or is the rotation itself
   broken?** Only a live check past the TTL answers this.
3. **Does Bloud want a second credential path on an app it already SSOs?**
   If no, Hermex is unsupported and the OpenAI-compatible route is the only
   third-party story.
4. **Is a per-app credential reveal in the Bloud dashboard in scope?**
   Without it a generated basic-auth password has nowhere honest to be shown.
5. **Does `install_id` survive a Bloud container recreate?** Hermex pins
   connection identity to it and refuses on mismatch (`.differentHost`). It
   lives in `config.yaml` (`config_defaults.py: "install_id": ""`), the file
   Bloud rewrites every pass. The configurator must preserve the key rather
   than strip it, or every recreate looks like a different host.
