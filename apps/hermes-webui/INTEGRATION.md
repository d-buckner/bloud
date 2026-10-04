# Hermes Web UI in Bloud

Upstream: <https://github.com/nesquena/hermes-webui> (MIT)
Image: `ghcr.io/nesquena/hermes-webui:0.52.113`
Agent source: `NousResearch/hermes-agent` `v2026.9.14` (MIT)

Everything below was measured on 2026-10-03 against that image under rootless
podman, on the same uid-mapping shape Bloud runs on. Where a claim comes
from reading upstream source rather than from running it, the file and line
are named so the next person can re-check it instead of trusting me.

## What the image actually is

The image is the **front end only**. The Hermes agent it drives is a Python
source tree that the container installs into the venv it builds on first
boot, from wherever that tree is mounted:

```
== Adding hermes-agent's pyproject.toml base dependencies to the virtual environment
```

Mount nothing and the app still starts, degraded, and says so out loud:

```
!! WARNING: hermes-agent source not found.
!!   Looked in: /home/hermeswebui/.hermes/hermes-agent
```

So a Bloud install that only pulls the image gives the user a chat window
with no agent behind it. `agent_source.go` exists to close that gap: it
installs the pinned agent tree into `{{appDataDir}}/agent-src`, and
metadata mounts that at `/home/hermeswebui/.hermes/hermes-agent`,
read-only. The upstream init script stages the tree to `/tmp/hermes-agent-build`
before installing, so a `:ro` mount is the supported shape, not a
workaround; it warns when the mount is writable instead.

## It shares Hermes' home (`integrations.agentHome`)

The image is a front end, so the app it fronts is a dependency, and the
catalog says so with a required `agentHome` integration whose default
provider is `hermes`. That makes the relationship a graph edge rather than
a paragraph of prose: Hermes converges ahead of this container, the
dependency shows in `docs/architecture/dependency-graph.md`, and the
install records the choice instead of leaving it implied.

The contract carries **no payload**. That is the whole design, not an
omission. A front end that shares the agent's `$HERMES_HOME` needs no
address, no port, and no bearer. It needs the same tree:

```yaml
# apps/hermes
- source: "{{dataDir}}/hermes/home"
  destination: /opt/data

# apps/hermes-webui
- source: "{{dataDir}}/hermes/home"
  destination: /home/hermeswebui/.hermes
```

One `config.yaml`, one memory, one skill set, one session store, one
`mcp_servers` map. An MCP server added for Hermes is in this UI on the
same pass with no wiring here at all, because this UI is not synchronising
with the agent's configuration -- it is reading it.

### This is upstream's own topology, not a Bloud invention

`/apptoo/docker-compose.two-container.yml` and
`docker-compose.three-container.yml` both mount one `hermes-home` volume
across the agent and the web UI:

> All three share the same hermes-home volume so config, sessions, skills,
> and memory are consistent across all surfaces.

Bloud's shared tree is that volume. The tree sits outside `apps/` because
it belongs to neither app, which is the same rule that puts `media/` and
`downloads/` outside `apps/`.

### Why this is not the gateway

The other way to connect a second front end is Hermes' `api_server`, an
OpenAI-compatible listener. It was tried and removed. A gateway flattens
the agent into a model endpoint: the front end posts a prompt and gets a
completion, and loses the sessions, the memory, the skills, and the tool
namespaces that make it a front end for *this* agent rather than for any
model behind a chat-completions URL.

Upstream keeps the gateway for its own purposes -- `HERMES_API_URL` feeds
the Tasks/System pill and cron ticking -- and that is the correct scope for
it: a status readout. It is the wrong scope for chat. Bloud wires neither,
because the shared home makes both unnecessary for the surfaces we ship.

### The uid alignment the sharing costs

Two containers on one tree must run as the same uid, or the first to chown
it locks the others out of their own config. The images disagree by
default: this image runs as `hermeswebui` (uid 1024), the agent as
`hermes` (uid 10000), and under rootless podman those are two different
host uids.

Both are set to 1000 in `metadata.yaml` -- `WANTED_UID`/`WANTED_GID` here,
`HERMES_UID`/`HERMES_GID` on the agent -- so both containers land on the
same host uid and the shared tree is writable by both. Upstream states the
same requirement in their compose files: "ALL containers that write to the
same host directory must run as the same UID/GID."

`WANTED_UID=0` is **not** a way through. It keeps the tree host-owned and
breaks the boot: the init script re-execs through `su` and the second
phase dies in `rsync: [Receiver] fork failed ... Resource temporarily
unavailable`. Uid 1000 is a normal privilege drop, which is what works.

### The web UI writes nothing to the shared config

`apps/hermes` is the only Bloud writer of `config.yaml`. This app's
configurator installs the agent source and reads back what the running app
resolved; it has no write path to the shared file. Two configurators
writing one file is a churn loop, and worse, it would let the front end
silently overwrite what the agent's own convergence produced.

The read-back is `GET /api/profiles` and `GET /api/mcp/servers`, both of
which report what the running server resolved out of the shared
`config.yaml`. `PostStart` logs both, so "one brain, two surfaces" is
visible in the reconciliation log rather than assumed.

## The agent source pin

| | |
|---|---|
| Repo | `NousResearch/hermes-agent` |
| Tag | `v2026.9.14` |
| URL | <https://github.com/NousResearch/hermes-agent/archive/refs/tags/v2026.9.14.zip> |
| SHA-256 | `c3694a72bf739c76718e31529102f0e4c16f225c2fba0bec3136ba92e127addc` |

Verified with `sha256sum` on the downloaded archive, 2026-10-03. The
marker file `.bloud-agent-source` written into the installed tree carries
**both** the tag and the digest, so a bump of either invalidates what is on
disk and the new tree gets installed. A marker that named only one of them
would let a digest change pass unnoticed, which is the half that matters
when upstream republishes a tag.

`verifyAgentSource` checks the staged tree contains `run_agent.py` and
`pyproject.toml` before the marker is written, so a truncated download
cannot mark itself installed.

## `HERMES_NIX_BUILD=1` is not optional

The agent's `setup.py` refuses a non-editable build outside a Nix sandbox.
Without the variable the container aborts during first boot:

```
RuntimeError: Building wheels or sdists for hermes-agent is not supported.
Hermes is distributed via the shell installer, Docker image, or Nix.
...
If you are building with Nix (uv2nix), this error should not fire -
the Hermes Nix derivation sets HERMES_NIX_BUILD=1. If it does, file a bug.
```

Measured: `Exited (1)`. The webui installs the agent exactly the way that
message forbids, so the opt-out has to be in the environment. It is set in
`metadata.yaml`, and the container boots.

## The agent-source directory has to be world-readable, not just present

The single hardest bug here, because nothing about it looks like a
permission problem until the container is already installed and healthy
looking.

`ensureAgentSource` creates `{{appDataDir}}/agent-src` with
`MkdirAll(0755)`. **The process umask applies to `MkdirAll`.** On a host
with umask `0077` the directory lands `0700`, owned by the host user. The
archive's own contents come out `755`/`664` because the zip carries those
modes, so a directory listing looks fine and only the root is wrong.

The container mounts that root and reads it as `hermeswebui`, uid 1024,
which is neither owner nor group. It cannot traverse into its own agent
tree, and the app dies at import time:

```
  File "/app/api/config.py", line 179, in _discover_agent_dir
    if path.exists() and (path / "run_agent.py").exists():
PermissionError: [Errno 13] Permission denied:
  '/home/hermeswebui/.hermes/hermes-agent/run_agent.py'
!! ERROR: hermes-webui failed or exited with an error
```

That is not an install failure and not a health-check timeout. The install
reports success, the container starts, the app exits 1, the restart policy
brings it back, and it dies again in about a second, forever. The health
check's 180-retry budget just watches.

`ensureReadableRoot` ORs `0o055` into the root's mode on **every** pass,
not only after an install, because the mode is set by whatever created the
directory and a fix that only runs on a fresh install leaves an existing
bad tree bad. `makeAgentSourceReadable` normalizes the whole tree once per
install, for the wider case of an archive that carried restrictive modes.

Verified on the real install: the directory went `700 → 755` on the
reconciliation pass that picked up the fix, and the container came up
healthy and stayed up.

## The data-directory permission contract

This is the part that looks like a Bloud bug and is not one.

The image's `Config.User` is `root`. Its init script drops privileges to
the `hermeswebui` user (uid 1024 in the image) and chowns `$HERMES_HOME`
to it. Under rootless podman, with the usual `0→1000, 1→100000` mapping,
container uid 1024 is **host uid 101023**. Once the agent has run:

```
$ stat -c '%a %u:%g %n' $BLOUD_DATA_DIR/apps/hermes-webui/data
700 101023 101023 ...
$ echo x > .../data/config.yaml
Permission denied
```

The host agent wrote that directory's first contents and can never write it
again. Three things look like fixes and are not:

- **`HERMES_CONTAINER=1`** does not exist in this image. Grepped: nothing
  in `/hermeswebui_init.bash`, nothing in `/apptoo`.
- **`HERMES_HOME_MODE`** exists, but it only governs the credential-file
  fixer in `api/startup.py:fix_credential_permissions`, which touches
  `.env`, `auth.json`, `.signing_key` and friends. It says nothing about
  the directory, and setting it does not make the directory writable.
- **`WANTED_UID` / `WANTED_GID`** do control the drop target, and this is
  the knob Bloud actually uses -- set to `1000`, not `0`, to line this
  container up with `HERMES_UID` on the agent so both can write the shared
  tree. Setting them to `0` is a different thing and does not work: it
  keeps the tree host-owned and breaks the boot, because the init script
  re-execs itself through `su` and the second phase dies in
  `rsync: [Receiver] fork failed ... Resource temporarily unavailable`.

So the rule that follows from all of this, for the app that *does* own the
file:

> **The host can write `config.yaml` before the agent's first start. After
> that, the write goes through the running container.**

That rule belongs to `apps/hermes`, which is the writer of the shared
`config.yaml`. This app has no write path to it at all, which is the point
of the shared home: the front end reads what the agent's own convergence
wrote rather than racing it.

The measurement above is still worth keeping, because it is what tells you
why a second writer would have to go through the container too, and why
two of them on one file is a churn loop rather than a feature.

## The app re-reads its config live

This is what makes the whole PostStart design work without a restart.
After an exec write, with the container running and untouched:

```
$ curl -s http://127.0.0.1:8787/api/profiles | jq '.profiles[0]'
{
  "name": "default",
  "model": "verified-model-5",
  "provider": "custom:bloud",
  ...
}
```

`/api/profiles` reports what the running server resolved out of
`config.yaml`, and it reflected the write with no restart. That is why
`PreStart` asks for a restart only for the agent source (a new tree has to
be installed at boot) and never for a config change.

## The model key is `default`, not `model`

The agent reads its selected model from `model.default` inside
`config.yaml`. Writing `model.model` does not work, and it does not fail
loudly either. This was the second of the two bugs that made the reverted
install useless, and it is the nastier of the pair because everything
upstream of the chat request looks healthy:

- the YAML is valid and parses;
- `/api/profiles` reports the model, because that endpoint reads the file
  back loosely;
- the provider resolves, the endpoint is dialed, and the auth is fine;
- the provider answers `HTTP 400: Invalid model name passed in model=`.

The app's own resolver is the authority here. `api/config.py`:

```python
model_cfg = active_cfg.get("model", {})
if isinstance(model_cfg, str):
    default_model = model_cfg.strip()
elif isinstance(model_cfg, dict):
    cfg_default = str(model_cfg.get("default") or "").strip()
```

and the app's own settings write path writes `model_cfg["default"]`. The
agent tree is looser, `model_cfg.get("default") or model_cfg.get("model")`,
so `model` survives there as a legacy alias, but the alias is not universal:
the webui resolver does not carry it, so a session is created with
`model: ""` and every turn dies on the empty model name.

Write the key every reader agrees on. `modelDefaultKey` names it in
`apps/hermes`, the one Bloud writer of this file format. The web UI reads
the same key through its own resolver and writes nothing, so there is no
second configurator left to drift.

## The venv's `.deps_installed` marker can poison an install

The image's entrypoint, `/hermeswebui_init.bash`, guards its dependency
install with a marker file:

```bash
if [ -f /app/venv/.deps_installed ]; then
  echo "== Dependencies already installed - skipping (fast restart)"
else
  uv pip install -r requirements.txt
  # ... then the agent's [all] extra, from the mounted source ...
  touch /app/venv/.deps_installed
fi
```

The marker is touched **whether or not the agent source was found**. So if
the first boot happens while the agent source is invisible - which is exactly
what the umask problem above produces - the entrypoint prints
`WARNING: hermes-agent source not found`, touches the marker anyway, and
the agent's dependencies are never installed. Every restart after that takes
the fast path and never retries. The app then serves, answers `/health` with
`ok`, reports the right provider on `/api/profiles`, and fails every chat
with:

```
AIAgent not available -- check that hermes-agent is on sys.path
python: /app/venv/bin/python
```

because `run_agent.py` needs `python-dotenv` and the venv has no agent
dependencies at all. Measured on the poisoned container: 58 packages in the
venv, no `dotenv`. After a clean install with the source readable: 271
packages, `Installed 98 packages` for the agent's extra.

The fix is upstream of the marker: keep the agent source visible at first
boot, which is what `ensureReadableRoot` on every pass buys. There is no
Bloud-side repair of an already-poisoned venv, because repairing it means
deleting the marker and restarting the container from a phase that is not
`PostStart`, and `PostStart` cannot ask for that restart. A poisoned venv
lives in the container's own writable layer, so recreating the container
(which an uninstall followed by an install does) clears it.

## Why the SSO strategy is forward-auth

The app has a native OIDC client, and it cannot be used on a home LAN. Its
outbound URL validator, `api/auth_oidc.py:_validate_outbound_oidc_url`,
rejects the issuer before it ever dials it:

```python
if parsed.scheme != "https":
    raise OIDCAuthError("OIDC endpoint URLs must use https", status_code=502)
...
if _is_disallowed_oidc_host(hostname):
    raise OIDCAuthError("OIDC endpoint URLs must not target private or local addresses", ...)
```

`_is_disallowed_oidc_ip` rejects loopback, private, link-local, multicast,
unspecified, and reserved. It resolves the hostname and checks the answers,
so this is not bypassable by naming. Bloud's issuer on a LAN deployment is
`http://sso.localhost:8080` or a LAN address with a locally issued
certificate: rejected twice over, once for the scheme and once for the
address family.

The app's own alternative login is a single shared password
(`HERMES_WEBUI_PASSWORD`), which is a worse door than Bloud's per-user one.
So the gate is the proxy's: **forward-auth** in front of the whole origin,
and the app's own password stays unset. Behind Bloud's outpost the app
never sees an unauthenticated request, and its own login is never reached.

## Health and the first-boot budget

`GET /health` returns `{"status":"ok", ...}` and is listed in the app's
own `PUBLIC_PATHS` (`api/auth.py:51`), so it answers without the app's
login and without a session. That makes it usable both as the container
health check and as `PostStart`'s verification.

The health check's retry budget is wide (180 retries) because first boot
builds the venv: the webui's requirements plus the agent's `[all]` extra,
from PyPI, inside the container. On a cold pull that is minutes, and a
tight budget would kill a healthy install in the middle of a working
download.

## Verification commands

The measurements above, as commands:

```sh
# The agent-source build refusal (omit HERMES_NIX_BUILD and watch it die)
podman run --rm -v $PWD:/home/hermeswebui/.hermes/hermes-agent:ro \
  ghcr.io/nesquena/hermes-webui:0.52.113

# Both containers land on one uid in the shared tree
podman exec <webui> id                      # uid=1000(...) after the drop
podman exec <hermes>  id hermes             # uid=1000(hermes) after the remap
stat -c '%a %u:%g' $BLOUD_DATA_DIR/hermes/home               # one owner, both containers

# The app's own account of the shared config it read
podman exec <webui> curl -s http://127.0.0.1:8787/api/profiles

# The proof the sharing works: the agent's MCP namespaces, seen from the UI
podman exec <webui> curl -s http://127.0.0.1:8787/api/mcp/servers
```

## Icon

`selfh.st/icons` does not carry `hermes-webui`, so the icon is upstream's
own `static/favicon-512.png` from the `hermes-webui` repository, taken
unmodified. `IconHandler` serves the file verbatim; nothing rescales it.

This one gets checked rather than assumed, because the obvious follow-up
("this app is a front end for Hermes, so it should wear Hermes' icon")
replaces a real asset with a different design. The two are not the same
mark:

| file | sha256 | what it is |
|---|---|---|
| `apps/hermes/icon.png` | `a2d912b2…` | `selfh.st/icons` `hermes-agent.png`, verbatim. Black line art on transparency. |
| `apps/hermes-webui/icon.png` | `771a8e1b…` | the image's own `/app/static/favicon-512.png`, verbatim. A filled, colored tile. |

The webui hash is byte-for-byte the file the published image ships, so this
is upstream's own artwork and not something invented here:

```
podman exec apps-hermes-webui sha256sum /app/static/favicon-512.png
771a8e1b322eb6afa12198ffc6fe220132c8474afb60dd9f00814fe82f9b574f  /app/static/favicon-512.png
```

Replacing it with `hermes-agent.png` would give the dashboard two adjacent
tiles wearing the same mark for two different things, and would drop the
webui's real icon for the agent's. It stays as upstream ships it. If that
call is ever revisited, the swap is `cp apps/hermes/icon.png
apps/hermes-webui/icon.png` and nothing else.

## Verified on a real install

Installed through the host-agent API on the native backend, no manual
container runs. The whole graph path: intent queue, dependency ordering,
agent-source install, container create, PreStart, PostStart, route
generation.

```
$ ./bloud install hermes-webui
==> Successfully installed hermes-webui

$ podman ps --filter name=apps-hermes-webui
apps-hermes-webui   Up 22 seconds   0.0.0.0:8787->8787/tcp

$ podman exec apps-hermes-webui curl -s http://127.0.0.1:8787/api/profiles
model = qwen3.8-flash-next   provider = custom:bloud

$ cat $BLOUD_DATA_DIR/apps/hermes-webui/data/config.yaml
cat: Permission denied          # the documented contract, not a bug
```

The provider and model came from Bloud's Settings -> AI, written by the
configurator and read back through the app's own endpoint. The host's
inability to read the file it wrote is the permission contract above
operating as designed.

The bar that actually matters is a chat turn, not a config read. With the
fixes in, a real round trip through the in-process agent and Bloud's
inference endpoint:

```
$ curl -X POST http://localhost:8787/api/session/new -d '{}'
  model = 'qwen3.8-flash-next'   provider = custom:bloud

$ curl -X POST http://localhost:8787/api/chat/start \
    -d '{"session_id":"<sid>","message":"Reply with exactly: PONG"}'
  effective_model_provider = custom:bloud

$ curl http://localhost:8787/api/session?session_id=<sid>
  user      : Reply with exactly: PONG
  assistant : PONG
  tokens    : 12859 in / 21 out
```

The token counts are the part worth reading: they are the provider's, not
the app's, so the turn really crossed the wire. Before the model-key fix the
same call returned `Model not found: ... model=`.

The forward-auth gate, exercised against the configured public host rather
than the localhost built-in:

```
$ curl -i -H 'Host: hermes-webui.home.thebloud.org' http://127.0.0.1:8080/
HTTP/1.1 302 Found
Location: https://home.thebloud.org/application/o/authorize/?client_id=...&redirect_uri=https%3A%2F%2Fhermes-webui.home.thebloud.org%2Foutpost.goauthentik.io%2Fcallback...
```

The unauthenticated request is redirected to Authentik with the app's own
proxy client and the original URL preserved in the outpost state, which is
the gate doing its job rather than the app answering for itself.

The ingress wiring, from the Traefik dynamic config the orchestrator
generated:

```yaml
hermes-webui:
  rule: "HostRegexp(`^hermes-webui\\.`)"
  middlewares: [hermes-webui-forwardauth]
  service: hermes-webui
hermes-webui-forwardauth:
  forwardAuth:
    address: "http://localhost:9001/outpost.goauthentik.io/auth/traefik"
    trustForwardHeader: true
```

## Known limits

- **The workspace is not the media library.** `/workspace` maps into this
  app's own tree rather than at Bloud's shared `media/` or `downloads/`.
  An agent that writes files should write them where its own backup
  covers them. Wiring it at the shared libraries is a deliberate future
  decision, not an oversight.
- **The model is Hermes' setting, not this app's.** The agent takes the one
  provider and the one model `apps/hermes` writes into the shared
  `config.yaml`. A model chosen inside this UI and Bloud's instance
  default are the same file's two keys, and the stickiness rule lives on
  the writer: `apps/hermes` never overwrites a `model.provider` it did
  not write. Making Bloud's default updatable without clobbering an
  operator's choice is an open question on that app, not on this one.
- **Two containers, one tree, one uid.** The sharing is only safe while
  `WANTED_UID` here and `HERMES_UID` on the agent agree. If either is
  changed alone, the container that starts second loses access to a tree
  the other one chowned. The failure is loud -- a `PermissionError` on
  `config.yaml` at import -- but it is a coupling an operator can break
  from two different places, and nothing in Bloud asserts the pair today.
- **`*.localhost` does not serve a forward-auth app under a public URL.**
  Traefik's routes are domain-agnostic, so the router matches, but the
  Authentik proxy outpost resolves the application by the request's host
  and has no binding for the built-in. The request comes back 404 from
  authentik (`domain_url: hermes-webui.localhost`) rather than redirecting.
  This is not specific to this app: it is every forward-auth app, and it is
  why the gate above was exercised with the configured public host. The
  e2e suite passes because it runs with the localhost origin as the
  instance's public URL, where the two agree.
