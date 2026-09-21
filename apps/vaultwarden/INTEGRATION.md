# Vaultwarden Integration

## Status: Working, with one caveat (plain HTTP)

[Vaultwarden](https://github.com/dani-garcia/vaultwarden) is a lightweight,
Bitwarden-compatible password manager. Bloud installs it as one container,
generates the environment file it needs, and wires its built-in OpenID Connect
support to the Bloud identity provider (Authentik).

**The caveat:** the Bitwarden web client refuses to talk to any server whose URL
is not `https://`, and Bloud serves apps over plain HTTP today. Until Bloud
serves HTTPS, the web vault works only with an opt-in development switch
(see [Plain HTTP](#plain-http)). Everything server-side works without it.

- Image: `docker.io/vaultwarden/server:1.37.3` (pinned; bundles web vault 2026.7.0)
- SSO strategy: `native-oidc` (callback `/identity/connect/oidc-signin`)
- Public URL: `http://vaultwarden.localhost:8080` (app subdomain on the Bloud base
  domain; `localhost:8222` direct for debugging)
- Database: Vaultwarden's own SQLite in `<appDataDir>/data`. No Postgres, no
  Redis, no sidecars.

## Architecture

### The generated env file

Vaultwarden's per-install settings cannot be expressed in a static manifest, so
`PreStart` writes `<appDataDir>/config/vaultwarden.env`, mounted read-only at
`/config/vaultwarden.env` and selected by the `ENV_FILE` variable in
`metadata.yaml`. The image loads it as a dotenv file, and its own healthcheck
script sources it as shell, so every value is single-quoted (both readers take
that verbatim; a test sources the rendered file to prove it).

| Key | When | Why |
|-----|------|-----|
| `DOMAIN` | always | The public URL. Vaultwarden derives its OIDC redirect URI (`<DOMAIN>/identity/connect/oidc-signin`) and the URLs it hands its clients from it. Built from the primary base URL with `configurator.AppExternalURL`, so a host change made in the UI takes effect on the next reconcile |
| `SIGNUPS_ALLOWED=false` | SSO wired | Closes local self-registration. Accounts come from the identity provider |
| `SSO_ENABLED`, `SSO_AUTHORITY`, `SSO_CLIENT_ID`, `SSO_CLIENT_SECRET`, `SSO_PKCE` | SSO wired | The provider. `SSO_AUTHORITY` is the issuer URL with its trailing slash: Vaultwarden compares it to the issuer claim exactly |
| `SSO_SCOPES` | SSO wired | `email profile offline_access` (`openid` is implicit, so listing it would send it twice) |
| `SSO_ONLY=true` | SSO wired | Turns off master-password login, and hides the "Other" login button and the "Create account" link |
| `BLOUD_DEV_ALLOW_HTTP` | dev switch on | Read by the container command, not by Vaultwarden (see [Plain HTTP](#plain-http)) |

The file is mode 0600 because it carries the OIDC client secret. Vaultwarden runs
as root in the container, which under rootless Podman is the host user that
writes the file, so it can read it. The orchestrator recreates the container
whenever the content changes.

Without SSO (Authentik not installed) only `DOMAIN` is written: there is no other
way to get an account or sign in, so signups and master-password login keep the
app's defaults. `SSO_ONLY` without a working provider would lock everyone out.

There is no admin panel (`ADMIN_TOKEN` is never set), so there is no internal
admin account to bootstrap.

### Sign-in policy

- **Signups:** `SIGNUPS_ALLOWED` and `SSO_SIGNUPS_ALLOWED` are separate switches.
  Closing the first does not stop identity provider users from being created on
  their first sign-in; the integration test signs in a fresh user with local
  registration closed.
- **`SSO_ONLY`:** refuses only the password login grant (`400 "SSO sign-in is
  required"`). The SSO code exchange and token refresh are unaffected, so
  existing sessions keep working, and the master password still unlocks the vault
  on the client.
- **A master password is still required.** SSO replaces the account login, not the
  vault key: Vaultwarden has no Key Connector or trusted-device support, so a
  first sign-in asks the user to set a master password and every later one asks
  for it to unlock.

### Extra scopes and token lifetime

Vaultwarden's SSO needs the `offline_access` scope (it keeps the session alive
with the provider's refresh token) and an access token that outlives the Bitwarden
web app's own 5 minute expiry check. Both are declared under `sso:` in
`metadata.yaml` (`scopes`, `accessTokenMinutes`), and the host-agent applies them
to the Authentik provider (`authentik.OIDCTuning`). `scopes` must stay in step
with `ssoScopes` in `configurator.go`; a test pins the two together.

The alternative, `SSO_AUTH_ONLY_NOT_SESSION=true`, would avoid both by using
Vaultwarden's own 30 day session instead of the provider's. It was not chosen
because a user disabled in Authentik would then keep a Vaultwarden session for up
to 30 days.

### What the login looks like

1. The user opens the app and sees the web vault's login page: an email field and
   a "Use single sign-on" button (nothing else, because of `SSO_ONLY`).
2. Typing an email and clicking the button sends the browser **straight to
   Authentik**. Vaultwarden emulates Bitwarden's domain lookup with a fixed fake
   organization identifier, so the client never asks the user for an SSO
   identifier. The email is only used to route to SSO: the account's email comes
   from the provider's `email` claim, which Bloud's verified-email scope mapping
   supplies (Vaultwarden refuses to create an account from an unverified email).
3. After the Authentik login the client lands on "set a master password" (first
   sign-in) or the lock screen (returning user).

`PostStart` verifies the wiring against the running app: `/alive`, then
`/identity/sso/prevalidate` (200 only if the env file was actually read), then
`/identity/connect/authorize`, whose redirect proves the issuer is reachable from
inside the container. The redirect URI itself is a response header that
`appclient` does not report, so the Go integration test and the browser journey
assert it.

## Plain HTTP

**The problem.** The Bitwarden web client (web vault 2026.7.0) throws `Insecure URL
not allowed. All URLs must use HTTPS.` unless the URL starts with `https://` or its
`isDev()` is true. There is no exception for `localhost`, and `isDev()` is a
build-time constant (`isDev(){return!1}` in `main.*.js`), not a setting. In a
browser this breaks not only SSO but every action past the login page, including
creating a local account. `window.isSecureContext` is true on `*.localhost`, but
the client checks the URL scheme, not the browser context, so that does not help.
The mobile, desktop, and extension clients were not tested; the web client also
validates its server URL field for `https://`, so assume they need HTTPS too.

**The real fix** is HTTPS in Bloud. It was verified: an unmodified Vaultwarden
behind a Traefik TLS entrypoint with a self-signed certificate and
`DOMAIN=https://...` passes the whole flow. What Bloud would need is a TLS
entrypoint and certificate handling, a scheme-aware base URL (it is `http://`
throughout `hostset`), the HTTPS callback URL registered with the provider, and a
certificate the user's browser trusts (including over an SSH forward). That is the
"reach-by-name and TLS layer" that `AGENTS.md` lists as planned work.

**The dev switch (until then).** Set `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1` on the
host-agent, for example `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1 ./bloud dev`. The CLI
forwards it to the host-agent (native, Lima, QEMU, and `./bloud e2e app`).

- `PreStart` writes `BLOUD_DEV_ALLOW_HTTP='true'` into the env file. The
  container's command in `metadata.yaml` looks for exactly that line and, if
  present, flips `isDev(){return!1}` to `isDev(){return!0}` in the container's own
  copy of the web vault before running `/start.sh`.
- **Off by default.** With the variable unset the command only runs `/start.sh`.
- **Localhost only.** The variable is ignored, with a warning, unless the app's
  public host is `localhost` or `*.localhost`, so a LAN or real-domain install can
  never be switched into it by an environment variable.
- **Fail closed.** If the switch is on but the bundle no longer contains the
  constant (an image bump changed it), the container refuses to start rather than
  run an unpatched client that would fail confusingly in the browser.
- **Restart safe.** An already patched bundle is accepted. Turning the switch off
  changes the env file, so the container is recreated from the pristine image.
- **Loud.** The host-agent logs a warning whenever it writes the file with the
  switch on, and the container prints one at start.

It is a security downgrade for a password manager (the vault's encrypted data and
session tokens cross the LAN as plain text), and the patched client is not what
upstream ships. It is for development and browser tests only. CI sets it for the
`vaultwarden` leg of `e2e-apps.yml`; the Playwright spec skips the rung that needs
it when the variable is not set.

## Verified constants

Checked on 2026-09-21 against `docker.io/vaultwarden/server:1.37.3`.

- Web vault: 2026.7.0 (`/api/config` reports it), bundled in the image at `/web-vault`.
- The constant the dev switch flips: exactly one occurrence in
  `/web-vault/app/main.<hash>.js`. Re-verify on every image bump:

  ```bash
  podman run --rm --entrypoint sh docker.io/vaultwarden/server:1.37.3 \
    -c 'grep -o "isDev(){return!1}" /web-vault/app/main.*.js | wc -l'   # expect 1
  ```

- Image facts: the app listens on port 80 in the container (`ROCKET_PORT`), runs as
  root, ships `curl` and its own `/healthcheck.sh` (which sources `ENV_FILE`), and
  declares no `HEALTHCHECK` metadata.
- Server behavior relied on (Vaultwarden 1.37.3 source): `SSO_SIGNUPS_ALLOWED`
  defaults to true and is separate from `SIGNUPS_ALLOWED`; `SSO_ONLY` refuses only
  `grant_type=password`; the `domain_hint` and `ssoToken` authorize parameters are
  ignored; `/identity/sso/prevalidate` answers 400 when SSO is off.

## Known limitations

- **One `DOMAIN`.** Vaultwarden takes a single public URL, so sign-in works on the
  primary host only. Bloud registers a redirect URI per host and per detected IP,
  but the app can only send users back to its own `DOMAIN`.
- **Uninstall leaves the Authentik provider and application behind.** This is a
  platform gap for every native-oidc and forward-auth app (`DeleteAppSSO` has no
  callers), not specific to Vaultwarden. Reinstalling reconciles the existing
  provider, including its scopes and token lifetime.
- **Only the web vault was tested.** The other Bitwarden clients probably need HTTPS
  as well, and the dev switch only patches the web vault. See [Plain HTTP](#plain-http).
- **Recreating an identity provider user locks that email out.** Vaultwarden links
  an account to the provider's user ID (`iss` plus `sub`) on the first sign-in. If
  the Authentik user is deleted and recreated with the same email, the new ID is
  refused (`existing SSO user ... with same email` in the container log) rather
  than taking the account over. Use a different email, or clear the app's data,
  to continue. It bites persistent dev instances and the Playwright spec's fixed
  test user, not a fresh CI runtime.
- **Local account linking by email.** `SSO_SIGNUPS_MATCH_EMAIL` (default true)
  links a first SSO sign-in to an existing local account with the same email. With
  local signup closed and Authentik emails operator-managed, this is acceptable.

## Testing

- `go test ./apps/vaultwarden`: rendering, the file's shell-safety, `PreStart`
  and `PostStart` against a fake app, the manifest and the constants agreeing, and
  the container command's shell wrapper run against a fake web vault.
- `go test -tags integration ./internal/e2e -run Vaultwarden` (see
  `docs/guides/contributing-apps.md`): install, the full SSO login and account
  creation against real Authentik, master-password login refused, token refresh,
  and uninstall cleanup. It talks to the server directly, so it needs no switch.
- `e2e/tests/vaultwarden.spec.ts` (Playwright): catalog, home tile, the
  SSO-only sign-in page, and the browser sign-in through to the vault (skipped
  unless `BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP` is set).
