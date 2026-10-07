# Client credentials

Some credentials are for machines and some are for people. Until now Bloud only
had the machine kind: an app declares what it provides, another app declares what
it consumes, and the orchestrator wires them. Nobody ever sees the value, and
that is correct for a PVR handing an API key to a companion.

A different case keeps coming up. A native phone app, a desktop sync tool, a
device with no browser: something that cannot join the identity provider and
needs a credential typed or pasted into it by a person. That needs a reveal
step, a rotation control, and an honest sentence about what the credential
opens. None of that exists in a machine-only contract.

**Client credentials are the pattern for that second audience.** A provider
declares that one of its published credentials may be handed to a client a human
holds, and Bloud mints it, delivers it, shows it, and lets the operator rotate
it.

## Declaring one

```yaml
# apps/<name>/metadata.yaml
provides:
  clientPassword:
    secrets:
      - password
    clientAccess:
      secret: password
      reveal: once
      rotate: bloud
      label: "Mobile client password"
      reaches: "this web UI as one shared account, with the same access as any signed-in user"
      snippet: url-and-password
```

| Field | Values | Meaning |
|---|---|---|
| `secret` | a name from `secrets:` | Which published credential the block describes. Required when the offer publishes more than one. |
| `reveal` | `never` (default) / `once` / `always` | How long the credential may be read back. |
| `rotate` | `bloud` / `provider` / `none` | Who can produce a new value. |
| `label` | free text | The short name shown where more than one credential is listed. |
| `reaches` | free text | What holding this credential grants. Required whenever `reveal` is not `never`. |
| `snippet` | `url-and-password` / `url-and-key` / `config-block` | The copy-ready form the reveal renders. |

An absent `clientAccess` block means no client access, which is what every
contract written before this field gets.

## What the loader refuses

`clientAccess` is held to a stricter bar than the rest of an offer. A
machine-only credential that is misconfigured shows up as a broken integration
and gets fixed. A human-facing one that is misconfigured shows up as a secret on
a screen with no truthful sentence about it, and the person holding it has no
way to find that out. So the load fails on:

- a block on an offer that publishes no secrets, which would reveal an empty string
- an ambiguous block when several secrets are published and none is named
- a `secret` the offer does not publish
- any unknown enum value, so a typo never silently reads as the safe default
- a revealable credential with no `reaches` disclosure

## Reveal

`GET /api/apps/{name}/client-credentials` returns descriptors. **It has no
value field at all**, at any reveal policy including `always`. That is the
structural guarantee behind reveal-once: the dashboard polls this endpoint, and
if the value could come back on it, "once" would be a policy about the modal
rather than about the API. The modal is not the security boundary.

`POST /api/apps/{name}/client-credentials/{secret}/reveal` returns the value
and the rendered snippet, and writes an audit record before the value goes out.

Under `reveal: once` the counter is bound to a SHA-256 fingerprint of the
value, not to the slot. That distinction is what makes the recovery path work:
rotate produces a value that has not been shown, so a lost password is
recoverable. A counter keyed on the slot would make a rotated credential
permanently unshowable.

## Rotate and revoke are different operations

This is the part most likely to be gotten wrong, and the reason they are
separate controls rather than one button.

**Rotate** changes what authenticates future sign-ins. It mints a new value,
stores it, and submits a reconcile intent; the resync re-runs `PreStart`,
renders a different config file, reports `RestartNeeded`, and recreates the
container. The API never touches a container, so the orchestrator stays the
single writer of side effects.

**Rotate does not end live sessions.** A session is signed under the app's own
persisted key and its stored record carries no reference to the credential that
created it. There is nothing to join a revoked credential against. The response
carries `sessionsSurvive: true` so the UI cannot quietly render a rotate as a
lockout.

**Revoke** ends every live session. `POST .../revoke` submits a
`RevokeClientSessionsIntent`; the orchestrator calls the configurator's
`RevokeSessions` through the container exec channel and forces a recreate,
because the app loaded its sessions into memory at boot and never re-reads the
file while it runs. The response is 202: the request is not the effect.

| | Rotate | Revoke |
|---|---|---|
| New sign-ins with the old value | Rejected | Unchanged |
| Sessions already running | **Keep working** until TTL | **End** |
| Use it when | You want a new value | A device must be locked out now |

## Why `reveal: once` is survivable

It is survivable only because of the rotate property above. Lose the password
and the phone keeps working, because its session is signed with the app's
signing key rather than derived from the password. Need a second device and
cannot recall the password: rotate, take the new pair, and the first device is
undisturbed.

Without that independence, reveal-once would be a trap: losing the password
would mean losing the device.

## `reveal: always` is gated on open debt

`always` means a long-lived plaintext read on the admin API. The admin position
is currently forgeable from any client that can add a header (the auth-bypass
entry in [operations/tech-debt.md](../operations/tech-debt.md)), so a provider
declaring `always` today is shipping a remote plaintext endpoint. Prefer `once`.

## The pilot

[client-credentials-hermes-webui-pilot.md](../plans/client-credentials-hermes-webui-pilot.md)
is the first app built on this pattern: hermes-webui, where a browser signs in
with OIDC and a phone signs in with a Bloud-minted password.

## API summary

| Method | Path | Returns |
|---|---|---|
| GET | `/api/apps/{name}/client-credentials` | Descriptors only, never a value |
| POST | `/api/apps/{name}/client-credentials/{secret}/reveal` | Value and snippet, admin only |
| POST | `/api/apps/{name}/client-credentials/{secret}/rotate` | New value, 200 with `sessionsSurvive` |
| POST | `/api/apps/{name}/client-credentials/{secret}/revoke` | 202 with `endsAllSessions` |
