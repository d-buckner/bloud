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
reference to the password that created it. That independence is what makes
`reveal: once` survivable: lose the password and a phone that is already signed
in keeps working.

It is also why **rotate and revoke are different controls**. Rotate changes the
password for future sign-ins; it does not end live sessions. Revoke clears the
session store and forces a recreate, which is the only way to end the sessions
the app has already loaded into memory.

## Credential delivery

`PreStart` writes `bloud.env` into `<appDataDir>/config`, mounted read-only at
`/config/bloud.env`. The container command sources it with `set -a` before
handing control to the image's own init. The secret never appears in the
container spec, so `podman inspect` on the host shows no password in the
environment.

The file is the delivery mechanism rather than container `environment` because
the values change and a running container's environment does not. A rotate is a
file rewrite plus a recreate.

## The data directory is world-writable

The image's init drops privileges to the `hermeswebui` user (uid 1024) and then
verifies `HERMES_WEBUI_STATE_DIR` is writable by touching a test file there.
The bind-mounted `<appDataDir>/data` directory is owned by the host agent (uid
1000), which maps to container uid 0, so a uid-1024 runtime cannot write a 0755
directory. `PreStart` sets it to `0777` on every pass, the same world-write
contract Hermes uses for its home directory (`HERMES_HOME_MODE: 0777`).

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
