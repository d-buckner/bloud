# Immich - Bloud Integration

## Port & Network

- **Port:** 2283
- **Network:** `apps-net`
- **Database:** Native shared PostgreSQL, database name `immich`
- **Cache:** Native shared Redis

## Containers

Immich runs as two user-scope Podman services:

- `podman-apps-immich-server` - web/API server and background workers
- `podman-apps-immich-machine-learning` - machine-learning model service

The server points `MACHINE_LEARNING_URL` at the ML container on `apps-net`.

## Database

Immich uses the shared native PostgreSQL service via the Unix socket mounted at `/run/postgresql`.

The Go configurator creates the `immich` database in `PreStart` and enables the `vector` extension for Immich search features. The Nix module adds `pgvector` to `services.postgresql.extraPlugins`.

## Redis

Immich uses the shared native Redis service via the Unix socket mounted at `/run/redis-bloud/redis.sock`.

Rootless Podman containers cannot reliably reach native host services through the user bridge, so Unix sockets are used for both PostgreSQL and Redis.

## SSO Integration

Immich supports native OIDC, but it is configured through the Immich admin API instead of environment variables.

Flow:

1. Authentik OAuth provider/application is generated from `metadata.yaml`.
2. The host-agent secrets manager persists the derived OAuth client secret.
3. `PostStart` creates the initial Immich admin account if needed.
4. `PostStart` logs in as the admin and updates `/api/system/config` with OAuth settings.

The SSO button is intentionally configured with `autoLaunch = false` so first-run setup and smoke tests do not get trapped in redirect loops.

## Routing

Primary embedded route:

```text
/embed/immich/
```

Traefik strips `/embed/immich` before forwarding to Immich.

Immich's browser OAuth callback is `/auth/login`, so `metadata.yaml` declares a root-level `absolutePaths` route for that path. This is an exception to the normal embedded-app routing rule and should stay limited to the callback path.

## Health Check

```text
/api/server/ping
```

Through Bloud routing, the integration test checks:

```text
/embed/immich/api/server/ping
```

## Data

Persistent directories under `~/.local/share/bloud/immich`:

- `upload` - uploaded photos and videos
- `model-cache` - downloaded machine-learning models

## Troubleshooting

Check service logs:

```bash
./bloud shell "journalctl --user -u podman-apps-immich-server -n 100 --no-pager"
./bloud shell "journalctl --user -u podman-apps-immich-machine-learning -n 100 --no-pager"
```

Check the database and extension:

```bash
./bloud shell "sudo -u postgres psql -d immich -c '\\dx vector'"
```

Check the embedded health endpoint:

```bash
curl -v http://localhost/embed/immich/api/server/ping
```
