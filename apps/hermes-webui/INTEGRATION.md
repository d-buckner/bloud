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
| `HERMES_WEBUI_GATEWAY_BASE_URL` | `Endpoint` |
| `HERMES_WEBUI_GATEWAY_API_KEY` | `APIKey` |

The graph edge the required integration produces orders Hermes ahead of this
app, so the bearer is published before `PreStart` reads it.

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
