# Hermes: Bloud integration notes

Upstream: [NousResearch/hermes-agent](https://github.com/NousResearch/hermes-agent)
(MIT). Bloud ships the official image `nousresearch/hermes-agent`, pinned to a
release tag. Hermes is a self-improving AI agent; the web dashboard is the
Bloud-facing surface.

## What Hermes is, in Bloud terms

Hermes is a single-container app whose image is an **s6-overlay stack**, not a
single process. The pieces that matter for the integration:

- The **web dashboard** (`hermes dashboard`) is the user-facing app. It runs as
  a supervised s6 service when `HERMES_DASHBOARD=1` and listens on
  `HERMES_DASHBOARD_PORT` (default 9119). Dashboard chat runs **in-process**:
  it reads `$HERMES_HOME` (config, memory, skills) directly and does not need a
  separate gateway server running.
- **Messaging gateways** (Telegram/Discord/etc.) are optional and registered
  dynamically by the user inside the app; Bloud neither configures nor needs
  them. They are out of scope for the install path.
- The container's **main program** (Docker CMD) is the interactive TUI when no
  arguments are given. Headless that TUI exits immediately. Bloud parks it with
  `command: ["sleep", "infinity"]` and lets the s6-supervised dashboard be the
  actual service. The orchestrator's `restartPolicy: always` keeps the whole
  stack up; s6 supervises the dashboard inside it.

## State / volume

`HERMES_HOME=/opt/data`, mounted from `{{appDataDir}}/data`. Hermes seeds its
own `config.yaml`, memory, skills, and SQLite session store there on first boot.
Bloud writes **only** the SSO keys into that `config.yaml` (see below); every
other key belongs to the user and is preserved untouched.

## The data-directory ownership contract (issue #136)

The host agent reads and rewrites `config.yaml` on **every** reconciliation
pass, and it shares that file with an app that reclaims it on every boot.
There are two separate ownership problems here, and the first one hides the
second: fixing the directory is necessary and not sufficient.

### Layer 1: the directory (`HERMES_CONTAINER`, `HERMES_HOME_MODE`)

`managedfile.Write` creates its temp file *inside* the target directory, so
it needs write access to `/opt/data`. The host agent does not have that by
default: the directory ends up owned by the container's uid mapped into the
rootless podman subuid range, and `chmod` against a subordinate uid is
`EPERM`, so nothing on the host side can repair it.

Hermes secures `$HERMES_HOME` itself, and its container probe misses podman:

```python
# hermes_cli/config.py


def _is_container():
    if os.environ.get("HERMES_CONTAINER") or os.environ.get("HERMES_SKIP_CHMOD") or os.path.exists("/.dockerenv"):
        return True
    # ... else: the cgroup markers "docker", "lxc", "kubepods"
```

Podman appears in none of those, so under podman Hermes treats the install as
a bare-metal one and applies the 0700 hardening to a bind mount whose host
owner is not the container's uid. The first write ever succeeds because Bloud
creates `config.yaml` before the container starts; every pass after that fails
with `permission denied` and the node parks in `error`.

The fix is the app's own opt-outs, both declared in `metadata.yaml`:

| Var | What it does |
|---|---|
| `HERMES_CONTAINER=1` | States what the probe cannot detect: podman is not in its marker list. Without it Hermes hardens `$HERMES_HOME` as if it were a bare-metal install. |
| `HERMES_HOME_MODE=0777` | The mode Hermes re-applies to `$HERMES_HOME` on **every** start. 0777 because the host agent is neither owner nor group member of the container's mapped subuid, so only the world-write bit lets the temp-file-plus-rename land. |

Measured on the real image under rootless podman with host uid 1000:

| Env | `$HERMES_HOME` after boot | after `podman restart` | host agent |
|---|---|---|---|
| neither | `0700`, owner `109999` | `0700` | read and write both `EACCES` |
| `HERMES_CONTAINER=1` | `0755` | `0755` | reads, cannot write |
| `HERMES_HOME_MODE=0777` | `0777` | `0777` | reads and writes |

The mode has to come from *inside* the container. A one-shot `chmod 0777` from
the host unblocks the next pass and Hermes re-tightens the directory on the
following container start, which is what makes a host-side repair a non-fix.

This is the class the LSIO apps in this catalog were solved with
(`apps/radarr`, `apps/sonarr`, `apps/qbittorrent`: config, media and
download dirs opened to `0777` because the container writes them as a host
subuid). The difference is that those apps never tighten the directory, so
`managedfile.EnsureWritable` from the host suffices. Hermes tightens it, so
the mode has to be declared to the app.

### Layer 2: the file (`stage2-hook.sh` chmods it to 0640)

With the directory shared the write path works and the read still does not.
The image's own init, `/opt/hermes/docker/stage2-hook.sh`, runs on every
container start:

```sh
# --- config.yaml permissions ---
# Ensure config.yaml is readable by the hermes runtime user even if it
# was edited on the host after initial ownership setup.
if [ -f "$HERMES_HOME/config.yaml" ]; then
    chown hermes:hermes "$HERMES_HOME/config.yaml" 2>/dev/null || true
    chmod 640 "$HERMES_HOME/config.yaml" 2>/dev/null || true
fi
```

The stated intent is exactly Bloud's situation: the file was edited on the
host. The mode chosen to serve that intent is the problem. Under rootless
podman `hermes:hermes` is host `109999:109999`, and `0640` grants nothing
to a host agent at uid/gid 1000. The file Bloud wrote becomes unreadable by
the thing that wrote it.

Measured with the real install ordering (host uid 1000, directory already
`0777`):

| step | result |
|---|---|
| Bloud pre-writes `config.yaml` `0644` owner `daniel` | ok |
| container boots | `0640`, owner `109999:109999` |
| host agent reads it | `EACCES` |
| host agent writes it (temp + rename in the `0777` dir) | ok |

So the agent can always write and can never read: the reverse of layer 1,
landing on the same terminal error.

It is the shell hook that sets the mode, not Hermes' Python. `save_config` →
`atomic_yaml_write` → `_mode_for_write` *preserves* the existing mode, so a
Python save could not turn a `0644` file into `0640`. Confirmed with a
`sitecustomize.py` audit hook wrapping `os.chmod` and `os.fchmod`: at the
first Python process of the boot the file is already `0640 uid=10000`,
before any `save_config` runs.

### The fix: write through `managedfile`, read through the container

The directory contract makes the write path work. The read goes through the
container, which reads its own file without complaint:

```go
raw, err := os.ReadFile(cfgPath)                       // the normal path
if errors.Is(err, fs.ErrPermission) && c.exec != nil { // the fallback
    out, err := c.exec(ctx, nodeName, nil,
        []string{"base64", containerHome + "/" + configFileName})
    // base64-decode `out` and merge over it
}
```

`Deps.Exec` is already in the configurator contract (`pkg/configurator`),
wired to `podman.Client.ExecWithEnv`, so this adds no new host capability.
The bytes come back base64-encoded on purpose: `Exec` is a combined
stdout+stderr channel, so a podman warning would otherwise land inside the
YAML. Encoded, contamination fails the decode instead of being merged and
written back over the operator's real config.

The container is reachable because the app runs with `restartPolicy: always`,
so podman brings it up at boot ahead of the host agent's first pass. Where
there is nothing to read through (no runtime wired, or the container is down)
the pass fails and says so.

Verified against a live install: `config.yaml` sits at `0640 109999:109999`,
the host read returns `EACCES`, and the node reconciles to `running` with the
SSO and inference blocks merged and the operator's own keys intact.

### Directions that do not work

**The uid mapping.** `--userns=keep-id` is the obvious candidate, and
it was measured rather than assumed:

| userns mode | container uid | host uid |
|---|---|---|
| default | 0 | 1000 (the host agent) |
| default | 10000 (hermes) | 109999 |
| `keep-id` | 1000 | 1000 |
| `keep-id:uid=10000` | 10000 | 1000 |

`keep-id:uid=10000` would work from the CLI and is not expressible through
the libpod HTTP create API the host agent uses: `userns.nsmode` accepts
`keep-id` but rejects the `:uid=` suffix, and `idmappings` is accepted and
then ignored. The `idmappings` result was re-checked with both field spellings
(`host_id`/`container_id` and `hostID`/`containerID`) and with `nsmode:
keep-id` alongside it: the create call returns an id, and a file chowned to
container uid 10000 still lands on host `109999`, with `UsernsMode` empty.
Plain `keep-id` plus `HERMES_UID=1000` is worse than the status quo: keep-id
injects the host user into the container's `/etc/passwd` at uid 1000, so
Hermes' own remap fails with `usermod: UID '1000' already exists`, hermes
stays at 10000, and the boot cannot even create `$HERMES_HOME/logs`.

**A shared group.** The file's group is the container's mapped subgid.
Making the host agent a member of it needs root, plus a group number derived
from the subgid range start, which differs per install. `HERMES_GID` cannot
reach it either: the mapping that would put the file's group at host gid 1000
only exists under `keep-id`, where the injected `daniel` group collides with
Hermes' `groupmod`.

**A symlink.** `refuse_symlinked_path` does make the hook skip the chown and
the chmod, but Hermes' `atomic_replace` is symlink-*preserving*, so the next
save replaces the symlink with a real `0640` file.

**Re-chmod from the host.** `EPERM`: you cannot chmod a file you do not own.
Replacing it works, and the next container start tightens it again.

**A read that cannot fall back stays a hard error.** When the host read is
refused and there is no container to read through, the pass fails rather than
carrying on with the last-known config: a Hermes running on a stale SSO block
looks healthy while the integration silently stops being updated. Both
failure messages name the mechanism and this file, because a bare
`permission denied` on a file the agent itself wrote reads like a Bloud bug
rather than the consequence of the app reclaiming its config at boot.

## The SSO contract (`sso: strategy: native-oidc`, `clientType: public`)

Hermes enforces its **own** auth gate on any non-loopback bind, and the gate
**fails closed**: a non-loopback dashboard refuses to start unless an auth
provider is registered. The upstream [self-hosted OIDC
provider](https://github.com/NousResearch/hermes-agent/blob/main/website/docs/user-guide/features/web-dashboard.md#self-hosted-oidc-provider)
authenticates the dashboard against any OIDC-compliant issuer via standard
authorization-code + PKCE, which is exactly what Bloud provisions in Authentik.
So Hermes joins the identity provider as a `native-oidc` app.

The provider is a **public PKCE client** (`sso.clientType: public`). This is a
Hermes constraint, not a Bloud choice: the self-hosted plugin supports only a
public client and rejects a `client_secret`. Bloud's OIDC provisioning defaults
to a confidential client for every app; the `clientType: public` field makes the
blueprint generator register a public, secret-less Authentik provider for this
app only.

### Why the issuer is the host loopback (`sso.loopbackIssuer`), and only on plain http

The self-hosted provider accepts an `https` issuer anywhere, but a plain `http`
issuer **only on a literal loopback hostname** (`localhost`, `127.0.0.1`,
`::1`). It validates the issuer at provider construction and every discovered
endpoint URL the same way, so Bloud's shared issuer host (`sso.localhost`) is
rejected: the provider never registers, and the dashboard then fails closed at
startup with `Refusing to bind dashboard to 0.0.0.0 ... but no auth providers
are registered`. That is exactly the failure the first CI run of this app's e2e
leg hit.

On a plain-http deployment Bloud therefore hands Hermes the **loopback issuer**,
set by the app's `sso.loopbackIssuer: true` (see
`internal/hostset.LoopbackIssuerBaseURL` and
`internal/engine/orchestrator.oidcInputsForApp`):

| Hermes `config.yaml` key | Value |
|---|---|
| `dashboard.oauth.provider` | `self-hosted` |
| `dashboard.oauth.self_hosted.issuer` | `http://localhost:8080/application/o/hermes/` |
| `dashboard.oauth.self_hosted.client_id` | `hermes-client` |
| `dashboard.oauth.self_hosted.scopes` | `openid profile email` |
| `dashboard.public_url` | the dashboard's public URL (`http://hermes.<host>:8080`) |

Reaching `localhost:8080` **inside** the container is what makes this work, and
that is why the container runs with `network: host`: only in the host network
namespace is `localhost` the machine running Traefik. In exchange:

- The dashboard binds the host loopback (`HERMES_DASHBOARD_HOST=127.0.0.1`), so
  it is reachable only through Traefik. It is never exposed on a network
  interface, and host networking cannot publish ports, so the app declares none.
- Traefik routes to `http://localhost:9119` (the generated app route uses the
  app port), which that loopback bind satisfies.
- The browser is sent to the issuer on `http://localhost:8080`. That is the same
  reach the `*.localhost` issuer host has: both resolve to the local machine, so
  Hermes SSO works from a browser on the Bloud machine, and from nowhere else.
  That is the price of the loopback issuer, and it is only payable while the
  deployment itself is plain http, where no accepted issuer a remote browser
  could reach exists.

**Under a https public URL the override is skipped** and Hermes gets the shared
issuer, which is the public origin: `https://<public host>/application/o/hermes/`.
Two things make that the right answer there. The provider accepts an https issuer
anywhere, so nothing in its validation forces the loopback. And the issuer string
is not only dialed by the container: it is also where an unauthenticated browser
is redirected, and for a remote visitor `localhost` is the visitor's own machine.
Handing out the loopback issuer on a proxied deployment produced exactly that
failure: `https://hermes.<host>/` bounced to
`http://localhost:8080/application/o/authorize?...`, a port on the visitor's
laptop, and the sign-in could never complete. `HostSet.LoopbackIssuerBaseURL`
returns `""` for a https public scheme, and `oidcInputsForApp` then leaves the
shared issuer in place. The switch is self-healing: the next reconciliation pass
sees a different issuer in the managed keys, rewrites `config.yaml`, and
recreates the container.

`public_url` does double duty. It makes the OIDC callback deterministic
(`<public_url>/auth/callback`, matching the `callbackPath: /auth/callback` the
host-agent registers with Authentik), and it is the **exact** non-loopback
`Host` header the dashboard's DNS-rebinding guard accepts. Setting a
non-loopback `public_url` also engages the auth gate even on a loopback bind,
which is what we want: the gate is satisfied by the self-hosted provider, so
every proxied request is authenticated.

The merge is **whole-document and semantically compared**: Hermes' unrelated
settings survive, and when the SSO keys already match on disk the configurator
reports `changed=false` so reconciliation never churns the file or needlessly
recreates the container. When SSO is turned off (Authentik removed), the managed
keys are **stripped** so a stale provider can't gate the dashboard on a dead
issuer.

> **Note:** `HERMES_DASHBOARD_INSECURE` does **not** disable the gate (removed
> upstream in a June 2026 hardening pass: unauthenticated public dashboards were
> an attack vector). It is accepted and ignored. Bloud never sets it; the gate
> stays on and is satisfied by the self-hosted OIDC provider.

### The refresh token is the session (`sso.scopes: [offline_access]`)

The dashboard session lifetime tracks the OIDC **access token's** `exp`
(`hermes_cli/dashboard_auth/request_utils.py:access_token_max_age`), and the
session cookie's `Max-Age` follows it. So the question "how long until I have
to sign in again?" is answered by the access token lifetime, unless a refresh
token exists to rotate the session silently.

Bloud shipped neither half of that. `managedScopes` was `"openid profile
email"` with no `offline_access`, so Authentik never issued a refresh token,
and the native-oidc provider default was `access_token_validity: minutes=5`. At
every five-minute boundary the middleware reached its refresh path with an
empty refresh token (`middleware.py:_attempt_refresh` begins `if not
refresh_token: return None`) and fell through to a fresh SSO round trip. The
audit trail is the signature:

```
{"event":"session_verify_failure","reason":"no_provider_recognises"}
{"event":"login_start","provider":"self-hosted","reason":"auto_sso"}
```

In a browser the auto-SSO bounce is a redirect nobody notices. In the macOS
Desktop app it is a login prompt, on a five-minute cadence. Upstream carries
the same report against Authentik as
[#90000](https://github.com/NousResearch/hermes-agent/issues/90000) ("The
failure occurs exactly 5 minutes after login, matching the Authentik access
token lifetime"), and the same missing-scope class against Google as #83294
and #100147.

**Both halves are required, and they are pinned together by a test.**

| Half | Where | What it does |
|---|---|---|
| Provider side | `metadata.yaml` `sso.scopes: [offline_access]` | Flows through `OIDCInputs.ExtraScopes` into `authentik.OIDCTuning.ExtraScopes`, which adds Authentik's `offline_access` scope mapping to the provider's `property_mappings`. `extraScopeMappings` fails the pass loudly if the mapping does not exist, so a typo cannot silently drop the scope. |
| Request side | `configurator.go` `managedScopes` | The scope string written into `dashboard.oauth.self_hosted.scopes`, which is what Hermes actually sends in the authorize request. |

A scope the app requests but the provider does not carry is dropped by the IdP
in silence and the refresh token never arrives. A scope the provider carries
that the app never asks for does nothing. `TestMetadata_ScopeSetAndLifetimeMatchTheConfigurator`
fails on either.

`accessTokenMinutes: 129600` (90 days) is the other half of the symptom. The
session tracks the access token, so this is how long a sign-in lasts before
anything has to be rotated at all. With `offline_access` in place the refresh
token can extend a session past it too; the long lifetime is what makes the
number the user experiences be 90 days rather than the provider default of 5
minutes.

An existing install picks both up on the next reconciliation pass without a
reinstall: `ensureProviderTuning` adds the scope mapping and patches
`access_token_validity` on the already-created provider, and the configurator
rewrites `config.yaml`. The one thing that does not retroactively fix is a
session already signed with the old short token; that client re-authenticates
once and gets the new lifetime.

## The inference provider contract (`providers.bloud` + `model.provider: custom:bloud`)

Bloud registers its inference endpoint as a named entry in Hermes' v12
`providers:` map and then has to *select* it. The selection is the part that
is easy to get wrong, and getting it wrong fails at agent init, not at config
write time, so nothing looks broken until a chat is opened:

> `agent init failed: No LLM provider configured. Run `hermes model` to select
> a provider, or run `hermes setup` for first-time configuration.`

**A named provider is addressed as `custom:<config key>`.** Bare `custom` is
not "whatever named provider is configured": it reads `OPENAI_BASE_URL` and
`OPENAI_API_KEY` from the environment and never consults `providers:` at all.
Measured against the live config with `resolve_runtime_provider`:

| `model.provider` | `model.model` | resolved `base_url` | key | `source` |
|---|---|---|---|---|
| `custom` | `bloud/qwen3.8-flash-next` | `https://openrouter.ai/api/v1` | empty | `env/config` |
| `custom:bloud` | `qwen3.8-flash-next` | `https://inference.thebloud.org` | set | `pool:custom:bloud` |

The first row is the bug: the request would have gone to OpenRouter with no
credential, and because nothing resolved, `agent_init` fell through to the
"No LLM provider configured" raise. The slug is derived from the **config
key**, not the display name (`custom_provider_slug` in
`hermes_cli/providers.py`), so renaming the display name does not break the
selection. The model slug carries no provider prefix.

**Never write `api:` into a `providers.<name>` entry.** It is a known key, but
the named-custom runtime resolver reads it as the endpoint URL. With
`api: openai-completions` present alongside a correct `base_url`, the
resolver returns `base_url = "openai-completions"`, the literal transport
string. The wire protocol belongs in `api_mode`, which is what Bloud writes
(`chat_completions`).

The entry Bloud writes, verified end to end by feeding the generated file
back through `resolve_runtime_provider`:

```yaml
providers:
  bloud:
    name: Bloud
    api_mode: chat_completions
    base_url: https://inference.thebloud.org
    api_key: <from the resolved inference binding>
    default_model: <binding default>
    discover_models: true
model:
  provider: custom:bloud
  model: <binding default>
```

`adoptDefaultModel` only writes the selection when Hermes has none of its
own, so an operator who picked a different provider keeps it. `discover_models`
keeps the model list live from the endpoint rather than a snapshot.

## Health check

`GET /api/health` on `:9119`, loopback. Upstream declares this path a **public
liveness route** (exempt from the auth gate so external uptime probes work), so
the health check never trips the gate: it proves the dashboard process is up
without needing a session.

## Pinning

The image is pinned to a `vYYYY.M.D` release tag (see `metadata.yaml`). Hermes
tags roughly weekly. Before bumping, re-read
`website/docs/user-guide/features/web-dashboard.md`
(`#self-hosted-oidc-provider`) and the `plugins/dashboard_auth/self_hosted`
plugin in the target tag, and confirm three things that this integration depends
on:

1. The issuer rule is still "https, or http only on a literal loopback
   hostname". A change here decides whether `sso.loopbackIssuer` is still
   needed.
2. The `dashboard.oauth.self_hosted.*` config keys, the `/auth/callback` path,
   and the `auth_providers` field of `/api/status` are unchanged.
3. The refresh path still rotates a session with the provider's refresh token.
   This is what keeps a sign-in lasting 90 days instead of one access-token
   lifetime, and upstream's handling of it is the origin of the constant
   re-authentication this integration works around. If a bump changes
   `access_token_max_age`, `_attempt_refresh`, or how `refresh_token_from`
   treats a rotated token, re-verify against a live install past the old
   boundary and watch for `session_verify_failure reason=no_provider_recognises`.

## Testing

Unit tests in `apps/hermes/configurator_test.go` cover the config-merge
contract: SSO on writes the self-hosted provider keys (issuer, client_id,
scopes) plus `public_url`; a second pass over an unchanged file reports
`changed=false` (no churn); the user's own keys survive the merge; SSO off
strips the managed keys and creates nothing when there is nothing to strip; a
corrupt existing file errors rather than being clobbered.
`TestMetadata_ScopeSetAndLifetimeMatchTheConfigurator` pins the two halves of
the refresh contract together: every scope in `metadata.yaml`'s `sso.scopes`
is one `managedScopes` requests, every non-default scope in `managedScopes` is
one the provider was told to carry, and `accessTokenMinutes` is well past the
5 minute Bloud default. PostStart is covered
against a fake dashboard: it passes when `/api/status` reports the gate on with
the `self-hosted` provider, fails when the provider is absent (e.g. only
`basic`), and skips the provider check when SSO is off.

The issuer choice itself is covered in the orchestrator:
`oidc_loopback_issuer_test.go` asserts a `sso.loopbackIssuer` app is handed
`http://localhost:8080/application/o/<app>/` on a plain-http deployment while a
normal native-oidc app keeps `sso.localhost`, that the loopback-issuer app gets
no `sso.localhost:host-gateway` extra host, and that under a https public URL
both apps get the public origin as their issuer and neither gets an extra host.

`e2e/tests/hermes.spec.ts` runs the user-visible contract against a real
install: the gate is on (an unauthenticated visitor is handed to a sign-in
surface: the dashboard's own chooser or, with a single registered provider, the
shared Authentik flow), and completing the Authentik login on the loopback
issuer origin returns the user to the authenticated dashboard. Not covered here:
the live OIDC round-trip's own PKCE exchange, which is upstream's surface, not
Bloud's.
