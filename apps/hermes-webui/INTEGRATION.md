# Hermes Web UI Integration

## Status: implemented, not yet exercised end to end

`hermes-webui` is the browser and mobile front end for the Hermes agent. It is
the pilot app for the client-credentials pattern: a browser signs in with the
identity provider, and a native client that cannot join the provider signs in
with a password Bloud mints and reveals once.

- Image: `ghcr.io/nesquena/hermes-webui:0.52.113` (pinned)
- SSO strategy: `native-oidc`, callback `/api/auth/oidc/callback`,
  confidential client
- Port: `8787` (internal; not published to the host)
- Backend: the Hermes agent over the `agentApi` contract, dialed through the
  routed `/v1` prefix on Hermes' subdomain
- Uploads: a shared inbox at `/bloud-hermes/inbox`, mounted identically in both
  containers
- Client credential: `clientPassword`, `reveal: once`, `rotate: bloud`

The browser SSO criterion (SC1) and the phone-connection criterion (SC2) have
**not** been exercised: SC1 needs a real `https` deployment, SC2 needs a
physical phone. The unit, contract, and catalog tests are green; the end-to-end
user journey is the open item.

## The https requirement

The app validates its own OIDC URLs and is strict about it. It requires
`https` and rejects a hostname that resolves to a local or loopback address.

That makes the stock dev VM unusable for the browser path: it serves plain
`http` on `:8080` with `sso.localhost`, which the app rejects on both the scheme
and the address family. There is **no dev flag** that relaxes this, and none is
added here: a flag would exist to let the pilot pass against an environment
where the criterion does not mean what it says.

To exercise SC1, deploy against a real `https` origin with a hostname that
resolves to a public address. See the pilot plan's "How to verify" for the
acceptable ways to close the criterion.

## The two credentials, and why they are not the same

The app has one browser-facing backend and one upstream credential of its own,
and it does not let them collapse into one.

- **The `agentApi` bearer** is this app's own upstream: it is what hermes-webui
  presents to the Hermes gateway when it dials `/v1`. It is never shown to a
  human and is useless to a phone, because a phone talks to hermes-webui, not
  to the gateway.
- **The `clientPassword`** is what a phone presents to hermes-webui. It is the
  value Bloud mints, stores, and reveals once.

They coexist because the app treats its auth methods as a set rather than a
switch: `is_auth_enabled()` is an OR across password, passkeys, OIDC, and
trusted-header auth. Enabling the password does not turn OIDC off.

## The client password is independent of the session

A session is an HMAC token signed under the app's own persisted signing key
(`.signing_key` under the state directory). The stored record carries no
reference to the password that created it. That independence is what makes the
recovery path clear: lose the password and rotate, which issues a new one you
can reveal and logs every device out in the process.

It is also why **rotate and revoke are different controls** even though
rotation ends sessions too. Rotate clears the session store and writes a new
password before the recreate, so the app comes back with no live sessions and
only the new credential. Revoke clears the session store without changing the
password, for the case where you want the same password but must lock a device
out now.

## Credential delivery

`PreStart` writes `bloud.env` into `<appDataDir>/config`, mounted read-only at
`/config/bloud.env`. The container command sources it with `set -a` before
handing control to the image's own init. The secret never appears in the
container spec, so `podman inspect` on the host shows no password in the
environment.

The file is the delivery mechanism rather than container `environment` because
the values change and a running container's environment does not. A rotate is a
file rewrite plus a recreate.

## The data directory is world-writable, until the container takes it over

The image's init drops privileges to the `hermeswebui` user (uid 1024) and then
verifies `HERMES_WEBUI_STATE_DIR` is writable by touching a test file there.
The bind-mounted `<appDataDir>/data` directory starts out owned by whoever runs
the host agent, which maps to container uid 0, so a uid-1024 runtime cannot
write a 0755 directory. `PreStart` widens it to `0777`, the same world-write
contract Hermes uses for its home directory (`HERMES_HOME_MODE: 0777`).

That is the first-boot story. It is not the story every later pass sees. The
image's init performs a uid/gid handoff and chowns its own state directory, so
from the first boot on the directory belongs to the container's runtime user,
which under rootless podman is a **subordinate uid on the host**: container uid
1024 lands on host uid 101023 under a `100000:65536` mapping (container uid 0
maps to the agent's own uid, so uid 1 and up start at the base of the range).
POSIX allows `chmod` only by the file's owner or by `CAP_FOWNER`, so an
unconditional `os.Chmod` there returns `EPERM` and keeps returning it. Since
`prepareDataDir` is the first thing `PreStart` does, that failure used to abort
the whole pass before the client password was minted and before `bloud.env` was
written: `ERROR` on a fresh install, and a permanently stale config behind a
`WARN` on every resync of a running app.

`prepareDataDir` therefore goes through `managedfile.EnsureWritable`, which is
the repo's answer for any path a container has taken over. It still widens the
directory whenever the agent can, and tolerates the `EPERM` when a foreign uid
owns the path and can write it. The tolerance is bounded rather than blanket: a
directory with no write bit at all still errors, because the container could not
write that either. No host-side workaround substitutes for the fix: `chmod` as
the agent is the same `EPERM`, `chmod` as root fixes the mode but not the
ownership so the next pass fails identically, and `chown` to the agent takes
the directory away from the process that actually writes it.

## The agent connection

The `agentApi` integration is `required: true` with `requires: [apiKey]`. The
configurator maps the resolved binding to three variables:

| Variable | Binding field |
|---|---|
| `HERMES_WEBUI_CHAT_BACKEND` | fixed `gateway` |
| `HERMES_WEBUI_GATEWAY_BASE_URL` | `Endpoint`, with the trailing `/v1` stripped |
| `HERMES_WEBUI_GATEWAY_API_KEY` | `APIKey` |
| `HERMES_WEBUI_DEFAULT_MODEL` | `ModelName` |

The graph edge the required integration produces orders Hermes ahead of this
app, so the bearer is published before `PreStart` reads it.

`gatewayRoot` strips the `/v1` segment deliberately: the app treats
`HERMES_WEBUI_GATEWAY_BASE_URL` as the gateway root and appends
`/v1/chat/completions` itself, so handing it the OpenAI base the contract
composes produces `/v1/v1/chat/completions` and a 404.

## The attachment inbox, and the umask that comes with it

Uploads are the one place this app touches the agent's filesystem, and it took
two changes to make them work.

The app writes the file and then hands the agent a **path**, not bytes:
`api/upload.py` returns the absolute destination and `static/messages.js` puts
that string into `[Attached files: ...]`. The agent opens it with its own file
tools, in a different container. So the inbox is a Bloud-owned shared tree,
`{{dataDir}}/shared-trees/hermes/inbox`, mounted at `/bloud-hermes/inbox` in
both containers, and `HERMES_WEBUI_ATTACHMENT_DIR` names that destination. Left
at its default the inbox is `$HERMES_WEBUI_STATE_DIR/attachments`, which is
`/data/attachments`: this container's private state directory, which Hermes has
never seen.

The mount alone was not enough. The image's init runs the server with **umask
0077**, so an upload lands as a `0700` session directory holding a `0600` file.
Hermes runs as uid 10000 and this app as uid 1024, and under rootless podman
neither maps to the other or to a shared group, so the agent could not traverse
the directory, let alone open the file.

Setting `umask 0022` in the container `command` does **not** work, and the first
attempt here did exactly that and still measured `0077` on the server process.
The image's init sets `umask 0077` itself at line 41 of `/hermeswebui_init.bash`
("keep init scratch files private"), and it runs that line twice: once in the
root phase, and again in the runtime phase after it re-execs itself through `su`
at line 292. Anything the container command sets is overwritten before the server
starts.

So the command runs a **patched copy** of the init script with that one line
rewritten to `umask 0022`, which makes the session directory `0755` and the file
`0644`. Relocating the script is safe: it uses `$0` only for its banner and for
the `su` re-exec, and the re-exec is precisely why the copy has to be the thing
that runs. It is exec'd directly rather than through `bash "$init"` so the script's
own shebang still governs it, and it needs its execute bit because `su` execs the
path: a non-executable copy fails with 126.

Two guards wrap the sed, and both check the thing that matters: the original must
hold exactly one bare `umask 0077`, and the copy must hold none. Asserting instead
that the output contains `umask 0022` would pass on an image that already carried
such a line while the `0077` line moved, which is the exact silent regression the
guard exists to prevent. The image is pinned, so the line can only move on a
deliberate bump, and a stale patch stops the container rather than quietly
returning every upload to `0600`.

The copy lives under `/run`, not `/tmp`, because `/tmp` is one of the roots
`/api/file/raw` serves, so anything written there is readable by every signed-in
user.

### What the wider umask costs

A umask belongs to the process, so patching it widens every file the server
creates, not just uploads. That matters here because the state directory is a host
bind mount Bloud holds at `0777` (it cannot chown it to the container's uid without
root), so a `0644` file inside it is readable by any local uid, including other
apps' containers.

What is safe: every credential this app writes is chmod'd explicitly and does not
lean on the umask. `api/auth.py` writes `.signing_key` and then `chmod(0o600)`, and
the same file does the session store and the login-attempt counter; `api/oauth.py`,
`api/passkeys.py` and `api/providers.py` do the same for OAuth tokens, passkey
state and provider env files. `fix_credential_permissions` in `api/startup.py`
re-strips world bits from `$HERMES_HOME` on every start.

What is not: the chat transcripts under `$HERMES_WEBUI_STATE_DIR/sessions` are
written with a plain `open()`, and `api/session_discoverability.py` says so out
loud ("mode matches the prior write_text (umask default)"). On an install that
already exists they stay private because `sessions/` was created under the old
`0077` and is `0700`, so nothing can traverse into it whatever the file mode is. On
a **fresh** install the app creates that directory under `0022`, so it lands `0755`
and the transcripts inside it are world-readable.

Bloud cannot fix that from outside: the directory is owned by the container's uid,
so a chmod from the host agent is EPERM, the same wall `prepareDataDir` already
documents. The clean shape is group-based rather than world-based, and it needs a
capability Bloud does not have: `--group-add` on the agent container plus a setgid
inbox would let `umask 0027` hand the agent group read while leaving `sessions/` at
`0750`. Until `container.Spec` grows that, this is the trade: uploads work,
credentials are untouched, and transcripts on a fresh install are readable by local
uids.

`PreStart` also creates the inbox at `1777`. The orchestrator creates mount sources
`0755` owned by the host agent, and uid 1024 is neither that owner nor a group
member, so without the world-write bit every upload is `EACCES` inside a mount that
looks perfectly healthy. The sticky bit is there because a world-writable directory
otherwise lets either side unlink the other's files, and this app's own delete API
reaches it. See `apps/hermes/INTEGRATION.md` for why it must be a plain `os.Chmod`.

What still does not work, so a later reader does not rediscover it: files the
**agent** produces cannot be shown or downloaded. This app's `/api/file/raw`
serves only `$HERMES_HOME`, `/tmp`, the last workspace, and
`MEDIA_ALLOWED_ROOTS`; here `$HERMES_HOME` is `/root/.hermes` (unset, absent) and
the last workspace is this container's own empty `/workspace`, so nothing that
lives under the agent's `/opt/data/workspace` is reachable. Fixing that needs
the agent's workspace mounted here too plus `MEDIA_ALLOWED_ROOTS`, and it is a
bigger trust question: this app ships `/api/file/delete`, `rename`, and `move`,
so anything mounted read-write is deletable by every signed-in user.

## Icon

`icon.png` is the upstream favicon, committed unchanged from the image's own
source repository:

```
https://raw.githubusercontent.com/nesquena/hermes-webui/master/static/favicon-512.png
```

The selfh.st set has no `hermes-webui` entry (its `hermes-agent` entry is the
agent, not this front end), so the upstream icon is kept and documented here
rather than invented.

## Testing

- Configurator: `cd apps && go test ./hermes-webui/...` (mint, idempotency,
  revoke exec, env rendering)
- Catalog/conformance: `cd apps && go test ./...` covers the app through the
  shared harness (`WithAgent: true`)
- Reveal/rotate/revoke API: `cd services/host-agent && go test ./internal/api/...`

## Validation strategy

`sso.strategy` is `native-oidc` for browser sign-in; the mobile client path is
a Bloud-minted password, which is not an SSO strategy the app joins. The
browser gate is the ingress strategy the browser sees.
