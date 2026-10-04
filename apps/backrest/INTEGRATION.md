# Backrest Integration

## Status: working, one deliberate trade-off (the published port)

[Backrest](https://github.com/garethgeorge/backrest) is a web UI and scheduler
for [restic](https://restic.net), the deduplicating, encrypted backup tool.
Bloud installs it as one container and seeds it with a repository and a plan
that covers every app's private data, so backups work with no setup.

- Image: `docker.io/garethgeorge/backrest:v1.14.1` (pinned; bundles a matching
  restic at `/bin/restic`, installed at image build time)
- Public URL: `http://backrest.localhost:8080` (app subdomain on the Bloud base
  domain; `localhost:9898` direct, see [Authentication](#authentication))
- SSO strategy: `forward-auth`
- State: `/data`, `/config`, `/cache`, and `/userdata` under `{{appDataDir}}`,
  plus the default repository under `<dataDir>/backups`

## What it backs up

The plan Bloud seeds covers `/bloud/apps`, the read-only mount of Bloud's
`$DATA_DIR/apps`. That is every app's private tree: config, SQLite databases,
bundled Postgres data directories, uploads, and everything else an app declares
itself, including apps installed after Backrest.

It excludes `/bloud/apps/backrest`. That path is the same directory as
Backrest's own `/data`, `/config`, and `/cache`, so without the exclusion each
snapshot would nest the operations database and copy the repository password
out of `config.json` into the repository it protects.

**Not included, on purpose:**

| Path | Why not |
|---|---|
| `$DATA_DIR/bloud.db` | Bloud's own SQLite state. Out of scope for "back up the app folders"; add it to the plan if a full-instance recovery is wanted. |
| `$DATA_DIR/secrets.json` | Derived secrets and the app credentials. Mounting the data root read-only would put it in the container. See [Native backups](#native-backups) for where this belongs. |
| `$DATA_DIR/media/`, `$DATA_DIR/downloads/` | Shared libraries, potentially very large, not app state. Add them in the UI if wanted. |

The read-only bind is the one deliberate exception to invariant 15 ("an app
never reads or writes another app's files"). A backup tool's purpose is to read
them, so the reach is granted, and it is granted read-only so the write side
stays closed. The exclusion above keeps the tool out of its own tree.

## The seeded config

Backrest reads JSON (parsed with protojson, so field names and enum values match
its proto schema exactly). `PreStart` writes `<appDataDir>/config/config.json`
once, when it is absent, and never touches it again. Backrest owns the file from
its first load: it rewrites it to add its multihost identity and to record every
repository, plan, schedule, and setting the user edits. Overwriting it on a
reconcile would silently discard all of that, so the seed is one-shot by design.
A clear-data uninstall or a fresh install in an empty data directory is the only
path back to a seed.

The file lives at 0600 because it carries the repository password. Backrest runs
as root inside the container, which under rootless podman is the host user that
wrote the file, so it can read it.

| Key | Value | Why |
|---|---|---|
| `version` | `6` | The config-format version this pinned image uses. Backrest rejects an unversioned non-empty config, and version 0 cannot migrate. Bump alongside the image pin. |
| `instance` | `bloud` | The snapshot identity shown in the UI. Only meaningful when a second Backrest is paired. |
| `auth.disabled` | `true` | The forward-auth proxy authenticates the browser. See [Authentication](#authentication). |
| `repos[0].id` | `bloud-local` | The seeded repository. |
| `repos[0].uri` | `/repos/bloud` | The container path of `<dataDir>/backups/bloud`. |
| `repos[0].password` | generated | A 32-byte URL-safe secret, stored in the host secret store under `appSecrets.backrest.published.resticPassword`. It stays private to this app: nothing declares it under `provides:`, so no binding carries it. |
| `repos[0].autoInitialize` | `true` | Runs `restic init` on the first backup, since a repository that does not exist cannot have a guid. |
| `repos[0].prunePolicy` | 30 days, last-run clock | Monthly prune, relative to the previous run so a machine that was off does not prune on every boot. |
| `plans[0].paths` | `[/bloud/apps]` | Every app's tree, in one plan. |
| `plans[0].excludes` | `[/bloud/apps/backrest]` | Keep the tool out of its own state. |
| `plans[0].schedule` | `0 2 * * *`, local clock | Daily at 02:00 local time. Editable in the UI. |
| `plans[0].retention` | daily 7, weekly 4, monthly 6 | Roughly six months of history, bounded. |

`PostStart` reads the config back through Backrest's API and logs the instance
and the repository and plan counts. It is best-effort: the health check already
proves the process is serving, and an operator who switches Backrest's own login
on in the UI makes the read answer 401. That is a valid configuration, not a
fault, so neither case moves the node to ERROR.

## Where the default repository lives, and why that is only a start

The seeded repository is `<dataDir>/backups/bloud`: on the same disk as the data
it protects. It survives a bad upgrade, an accidental delete, or a corrupted
app, but not the disk failing. It is a starting point that needs no
configuration, and the password is stored so it stays usable.

A real deployment wants a second destination away from the machine. Backrest
supports S3, Backblaze B2, SFTP, rclone remotes, and mounted disks; add one in
the UI (**Add Repo**) and a plan (**Add Plan**) that points at `/bloud/apps`.
Nothing in Bloud has to change, and the local repository can stay as the fast
first hop.

## Authentication

Backrest has no OIDC support and no header-trust mode, so the browser gate is
the proxy's: forward-auth in front of the whole `backrest.<host>` origin, the
same shape as Calino and the Servarr apps. The seeded config disables
Backrest's own login, because a second login would ask for a password nothing
in the dashboard surfaces to the operator.

**The trade-off, stated plainly.** Bloud routes every app through Traefik at
`http://localhost:<port>`, so each app's port is published on the host, and that
path does not pass the forward-auth gate. On the LAN, `<host>:9898` is an
unauthenticated Backrest that can restore, delete, and reconfigure backups.
This is the same trust boundary as every other published app port, and it is
accepted deliberately here rather than silently: it is the price of borrowing a
foreign UI that cannot join Bloud's identity provider. The operator can enable
Backrest's own login in its UI (the configurator tolerates it), which protects
the direct port at the cost of a second login.

## Restoring

Restore is Backrest's, and it works while the Bloud dashboard is down: the
container is independent, and `restic` can also be run by hand against the
repository at `<dataDir>/backups/bloud` with the stored password. From the UI,
**Snapshot Browser** shows the file tree and restores files or directories. An
app's data directory (`/bloud/apps/<app>`) restores to the same paths it was
captured from, so a restore followed by reinstalling the app is enough to bring
it back.

Because the repository is standard restic, the data is not hostage to Backrest.
If the app is ever replaced by a built-in backup feature, the repository and its
password remain valid.

## Verified constants

- Image tag: `v1.14.1` (checked 2026-07-12, the latest release at the time).
- Config format version `6`: `internal/config/migrations/migrations.go`,
  `CurrentVersion = len(migrations)`.
- The image's config path defaults come from
  `cmd/docker-entrypoint` (`BACKREST_DATA=/data`,
  `BACKREST_CONFIG=/config/config.json`, `XDG_CACHE_HOME=/cache`); the manifest
  sets them explicitly so the mounts and the container agree.
- Icon: `https://cdn.jsdelivr.net/gh/selfhst/icons@main/png/backrest.png`,
  committed unchanged per the app-contribution guide.

## Native backups

The catalog app is the bridge, not the destination. Backups are foundational
enough that Bloud should own the surface: host-agent orchestrating restic, the
dashboard's Settings page owning destination, schedule, retention, status, and
restore, and one auth model instead of a second published port. Because
Backrest writes standard restic repositories, that work can adopt the
repository and password this app creates rather than starting over. The design
is tracked outside this document; see the repository's `docs/plans/`.
