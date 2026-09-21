# Paperless-ngx Integration

## Status: Complete (e2e-verified)

[Paperless-ngx](https://github.com/paperless-ngx/paperless-ngx) turns scans and
PDFs into a searchable archive: OCR, full-text search, tags, correspondents,
and a REST API. Bloud installs it as five containers, generates the
configuration file it needs, wires django-allauth's OpenID Connect provider to
the Bloud identity provider (Authentik), and creates the internal admin
account.

- Image: `ghcr.io/paperless-ngx/paperless-ngx:3.2.0` (pinned)
- Sidecars: `postgres:18`, `redis:7-alpine`, `gotenberg/gotenberg:8.37`,
  `apache/tika:3.3.1.0` (all pinned)
- SSO strategy: `native-oidc` (callback `/accounts/oidc/bloud/login/callback/`)
- Public URL: `http://paperless.localhost:8080` (app subdomain on the Bloud
  base domain; `paperless.<host>:8000` direct for debugging)

## Architecture

### Container graph

```
apps-paperless-postgres  (postgres 18)      ─┐
apps-paperless-redis     (redis 7)          ─┼─> apps-paperless (webserver, :8000)
apps-paperless-gotenberg (gotenberg 8.37)   ─┤   dependsOn postgres + redis
apps-paperless-tika      (tika 3.3.1.0)     ─┘
```

Apps own their infrastructure (repo invariant): Paperless-ngx declares its own
PostgreSQL, Redis (the Celery broker), and the two document-conversion
sidecars. Only the webserver joins `apps-net` (so Traefik can route to it); all
five share the `paperless-internal` network.

Gotenberg and Tika give Paperless-ngx Office (`.docx`, `.xlsx`, `.pptx`) and
email (`.eml`) ingestion: Gotenberg converts the document to PDF, Tika extracts
text from that PDF. PDFs and images need neither, which is why the webserver
only depends on postgres and redis: the sidecars are consulted per document,
not at boot.

Ports: `8000` is the container's HTTP port (upstream default). Paperless-ngx
binds `::` inside the container, so the healthcheck and the configurator both
reach it on `localhost:8000` from the host.

### The generated config file

Paperless-ngx is configured through environment variables or a
`paperless.conf` file it loads with `load_dotenv` before reading its settings
(`PAPERLESS_CONFIGURATION_PATH` names the file). The static settings live in
`metadata.yaml`; the configurator generates the rest, because they are
per-install:

| Key | Why it is generated |
|-----|---------------------|
| `PAPERLESS_URL` | The app's public URL (`paperless.<host>:<port>`), host-set aware |
| `PAPERLESS_ACCOUNT_DEFAULT_HTTP_PROTOCOL` | The scheme of that URL. allauth builds the OIDC redirect URI with it and defaults to `https`, which would not match the URI Bloud registers with the identity provider (`http://<app>.<host>:8080` today) |
| `PAPERLESS_SECRET_KEY` | Django's signing key. Required in v3: the app refuses to start without it |
| `PAPERLESS_ADMIN_USER` / `_MAIL` / `_PASSWORD` | The internal admin (see below) |
| `PAPERLESS_APPS` | Adds `allauth.socialaccount.providers.openid_connect` to `INSTALLED_APPS` |
| `PAPERLESS_SOCIALACCOUNT_PROVIDERS` | The OIDC client id, secret, issuer discovery URL, and scopes |
| `PAPERLESS_SOCIAL_AUTO_SIGNUP` / `PAPERLESS_SOCIALACCOUNT_ALLOW_SIGNUPS` | First-login account creation |
| `PAPERLESS_ACCOUNT_DEFAULT_HTTP_PROTOCOL` | Scheme for generated URLs, including the OIDC redirect URI |
| `PAPERLESS_LOGOUT_REDIRECT_URL` | Ends the session at the issuer instead of the app's sign-in page |

Notes that shaped the implementation:

- **The secret key is persisted in the file.** Paperless-ngx signs sessions and
  API tokens with it, so `PreStart` reads the existing value back and reuses it;
  only a missing key is regenerated (which signs everyone out). Rendering is
  deterministic, so an unchanged config never churns the file, and the
  orchestrator only recreates the container when the content actually changes.
- **The file is mode 0644 and the mount is read-only.** Under rootless Podman
  the container's `paperless` user (uid 1000) is a *subuid* on the host, so a
  host-written 0600 file (owner uid 501 in the dev VM) is unreadable inside the
  container. The file lives in `<appDataDir>/config`, which the app never
  chowns, so the host-agent can keep rewriting it. The app's own data
  directories (`data`, `media`, `consume`, `export`) are chowned by the image's
  init to its unprivileged user, which is why nothing Bloud must edit later
  lives there.
- **Values are single-quoted dotenv syntax**, which `load_dotenv` reads
  verbatim, so the provider JSON and URLs survive unmangled. Real environment
  variables still win over the file, which is why the connection settings stay
  in the manifest.

### OIDC login flow

1. An unauthenticated request is redirected to `/accounts/login/`, which
   renders the username/password form plus one button per configured allauth
   provider (`Bloud SSO`).
2. That button submits a form whose action is `/accounts/oidc/bloud/login/?process=`;
   allauth renders a form rather than a link because the POST is what starts the
   flow (login CSRF protection; `SOCIALACCOUNT_LOGIN_ON_GET` is off). The user
   journey is therefore a single click.
3. The app redirects to the issuer's authorization endpoint. allauth fetches
   `http://sso.localhost:8080/application/o/paperless/.well-known/openid-configuration`
   from inside the container (the `sso.localhost:host-gateway` extra host makes
   that resolve to Traefik, the same name the browser uses) and uses
   authorization-code + PKCE.
4. Authentik authenticates the user, the browser returns to
   `/accounts/oidc/bloud/login/callback/`, and allauth creates the Paperless-ngx
   account on first sign-in (matched by the `email` claim, which Bloud's
   verified-email scope mapping supplies).
5. PostStart verifies the wiring from the host: the sign-in page advertises the
   provider login URL, submitting that form hands the browser to the issuer
   (which is what proves discovery works from inside the container), and the
   internal admin authenticates against `/api/token/`. The redirect URI itself
   is a response header, so it is asserted by the Go integration test and by the
   browser journey rather than by PostStart.

`PAPERLESS_ALLAUTH_TRUSTED_PROXY_COUNT=1` is set in the manifest: Traefik is
the app's only proxy hop, and without it allauth would treat the proxy's
address as every client's address and its login rate limiter could answer 403.

### Internal admin account

Paperless-ngx has no admin API, and the image's `PAPERLESS_ADMIN_*` variables
are read by its s6 init script from the process environment, not from the
config file, so they cannot carry a per-install password. The account that owns
the instance is instead the one its **signup form** creates: the account
adapter promotes the first signup to superuser and closes signups afterwards.
PostStart therefore drives that form once per fresh install (CSRF token from the
page, posted back with the cookie it was issued against) with the password from
the host's `secrets.json`; later reconciliations find signups closed and only
verify that the account still authenticates.

The account is `bloud-admin` and never appears in the UI flow: every end user
arrives through SSO. It exists so `/admin/` and the REST API are reachable
without the identity provider, and so the sign-in page stops forwarding to the
(closed) signup page, which would otherwise hide the SSO button.

Local username/password login stays enabled (upstream default): it is the way
in if the identity provider is unavailable.

## Files

| File | Purpose |
|------|---------|
| `apps/paperless/metadata.yaml` | Five containers, native-oidc SSO, port 8000, static connection settings |
| `apps/paperless/configurator.go` | Config file generation (secret key, admin, OIDC provider document), PostStart verification |
| `apps/paperless/api.go` | Typed client: sign-in page, provider flow, signup, API token login |
| `apps/paperless/configurator_test.go` | Unit tests: rendering, secret-key persistence, admin/provider verification |
| `services/host-agent/internal/e2e/paperless_test.go` | Go integration tests (install, configure, ingest, uninstall) |
| `e2e/tests/paperless.spec.ts` | Playwright user journey (home tile → SSO → dashboard) |

## Key API endpoints (webserver, :8000)

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/accounts/login/` | GET | Sign-in page (renders the provider button) |
| `/accounts/oidc/bloud/login/` | POST | Starts the authorization flow (the SSO button's form action) |
| `/accounts/signup/` | GET/POST | First-run account creation (open only while no user exists) |
| `/accounts/oidc/bloud/login/callback/` | GET | OIDC redirect target (code exchange) |
| `/api/token/` | POST | Username/password to API token (the admin check) |
| `/api/documents/` | GET | Document list (used by the integration test) |
| `/api/documents/post_document/` | POST | Upload for consumption (multipart) |

## Verification

```bash
# User journey (requires a running dev runtime + installed user):
cd e2e && npx playwright test paperless

# Full Go integration path (fresh VM, real install/reconcile/ingest/uninstall):
./bloud validate --tier integration
```

Behavioral assertions (not config values): the Playwright spec signs in through
a real Authentik login and then checks the session authenticates the app's own
API; the Go integration test drives the allauth POST to the issuer's authorize
endpoint (proving discovery works from inside the container and the registered
redirect URI matches), authenticates as the internal admin, and uploads a text
document that must come back with its extracted content, which covers the redis
→ Celery → parser → search-index path. Uninstall then asserts that every
container, the data directory, and the routes are gone.

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| Container exits with `django.core.exceptions.ImproperlyConfigured: PAPERLESS_SECRET_KEY is not set` | The generated config file is missing or unreadable: check `<appDataDir>/config/paperless.conf` exists, is mode 0644, and that `PAPERLESS_CONFIGURATION_PATH` points at its in-container path. |
| Container exits with `password authentication failed for user "paperless"` | The database password did not reach the app. Paperless-ngx reads `PAPERLESS_DBPASS` (not `PAPERLESS_DBPASSWORD`); the manifest must keep using `{{postgresPassword}}` for both containers. |
| Sign-in page has no SSO button | The provider document did not load. Check `PAPERLESS_APPS` and `PAPERLESS_SOCIALACCOUNT_PROVIDERS` in the generated file, then the container log for a JSON parse error. |
| Login fails with `invalid redirect_uri` at the issuer | The redirect URI the app sent does not match the registered one. Check `PAPERLESS_ACCOUNT_DEFAULT_HTTP_PROTOCOL` matches the scheme of `PAPERLESS_URL` (both are generated), and that `sso.callbackPath` in `metadata.yaml` still matches allauth's path for the provider id. |
| Sign-in page says "Sign Up Closed" and shows no SSO button | No user exists yet, so `FIRST_INSTALL` forwards to the signup page while signups are open only on a fresh install. The bootstrap did not run: look for `internal admin account created` in the host-agent log. |
| Login fails with `invalid_client` at the callback | The issuer rejected the client credentials or the token endpoint auth method. Add `settings.token_auth_method` (`client_secret_basic` or `client_secret_post`) to `PAPERLESS_SOCIALACCOUNT_PROVIDERS`. |
| `403 Forbidden` on login after repeated attempts | allauth's login rate limiting sees the proxy as the client. `PAPERLESS_ALLAUTH_TRUSTED_PROXY_COUNT=1` (already set) must stay, or set `PAPERLESS_TRUSTED_PROXIES`. |
| Uploaded `.docx` or `.eml` never finishes consuming | Tika or Gotenberg is not up. Check `apps-paperless-tika` and `apps-paperless-gotenberg` (`podman logs`), then the webserver's `PAPERLESS_TIKA_*` settings. |
| `PostStart failed: ... sign-in page` | The generated config file did not reach the running app; the previous reconciliation may have failed before writing it. Re-run (uninstall/install or restart) and watch for `wrote Paperless-ngx config file` in the host-agent log. |
| Data directory survives a `clearData` uninstall | The app's data volumes are emptied from inside the container before removal, so an app whose containers are stopped cannot release them. Start the app once, then uninstall again. |
