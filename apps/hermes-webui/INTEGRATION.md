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
- **`WANTED_UID` / `WANTED_GID`** do control the drop target, and setting
  them to `0` does keep the tree host-owned. It also breaks the boot: the
  init script re-execs itself through `su`, and the second phase dies in
  `rsync: [Receiver] fork failed ... Resource temporarily unavailable`.
  Not a usable escape hatch.

So the rule the configurator follows is a simple one:

> **The host can write `config.yaml` before the agent's first start. After
> that, the write goes through the running container.**

`PreStart` seeds the file while the host still owns the tree, which is what
makes a fresh install come up with the instance's model already wired.
`PostStart` does the real convergence against a live container, because
the PostStart resync is the only thing that re-runs on a steady-state
running node, and a change to Settings -> AI has to reach an app that was
installed weeks ago.

### Writing through the container

`Deps.Exec` has no stdin channel, so the document travels inside the
command line, base64-encoded. The base64 alphabet contains no quote, so
the single quotes around it need no escaping and no byte of the YAML can
reach the shell.

The mode needs setting explicitly, and this is the trap that cost the most
time. A shell redirection **does not change the mode of a file that
already exists**; it only truncates. Writing into a pre-existing
root-owned `0600` file left it `0600`, and the app, running as uid 1024,
could not read its own config. The shipped command sets the mode after the
write:

```sh
umask 022
printf %s '<base64>' | base64 -d > /home/hermeswebui/.hermes/config.yaml
chmod 644 /home/hermeswebui/.hermes/config.yaml
```

`644` is `managedfile.ModeSharedConfig`, and it is right here for the same
reason it is right on the host side: the writer and the reader are
different uids, and the file's readability is bounded by the `0700`
directory it lives in, not by its own mode.

Verified from the worst case, a pre-existing `0600 root` file:

```
PRE:  600 root /home/hermeswebui/.hermes/config.yaml
POST: 644 root /home/hermeswebui/.hermes/config.yaml
```

### Reading it back

The same asymmetry runs the other way: once the app has rewritten its own
config (any settings change does), the file is `0600` and the host cannot
read it. `readConfig` falls back to `base64` inside the container, which
reads its own file without complaint.

The read comes back encoded on purpose too. `Deps.Exec` merges stdout and
stderr, so a runtime warning printed during the read would otherwise land
inside the YAML and get written back over the real config. Encoded,
contamination fails the decode instead of being merged.

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

# Who owns the home after the agent has run
podman exec <ctr> id hermeswebui            # uid=1024(hermeswebui)
stat -c '%a %u:%g' $BLOUD_DATA_DIR/apps/hermes-webui/data   # 700, a subuid

# The app's own account of the config it read
podman exec <ctr> curl -s http://127.0.0.1:8787/api/profiles

# The refusal and the escape
echo x > $BLOUD_DATA_DIR/apps/hermes-webui/data/config.yaml   # Permission denied
podman exec <ctr> sh -c "umask 022; printf %s '<b64>' | base64 -d > \
  /home/hermeswebui/.hermes/config.yaml; chmod 644 /home/hermeswebui/.hermes/config.yaml"
```

## Icon

`selfh.st/icons` does not carry `hermes-webui`, so the icon is upstream's
own `static/favicon-512.png` from the `hermes-webui` repository, taken
unmodified. `IconHandler` serves the file verbatim; nothing rescales it.

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
- **One inference binding.** The agent takes one provider from Bloud; the
  instance's other AI settings are not mapped.
- **A model chosen inside the app outranks Bloud's default.** The
  configurator never overwrites a `model.provider` it did not write, and
  `PostStart` warns rather than fails when the active profile is not
  Bloud's.
