# Backup, restore, and update

Bloud keeps all of its state in one directory, `BLOUD_DATA_DIR`
(`/var/lib/bloud` for the packaged install):

```
/var/lib/bloud
├── bloud.db                 Bloud's own state (SQLite)
├── secrets.json
├── host-agent-api-token
├── traefik/                 generated proxy config
├── apps/                    every app's private tree
├── media/                   shared: movies, shows, music
└── downloads/               shared: the media stack's drop area
```

Three scripts at the repository root operate on that directory. They are
`/bin/sh`, expect to run through `sudo`, and read the same environment
variables as the host-agent (`BLOUD_DATA_DIR`, `BLOUD_BACKUP_DIR`,
`BLOUD_PORT`, ...).

## `backup.sh`

```bash
sudo ./backup.sh
sudo ./backup.sh --include-shared --keep 7
sudo ./backup.sh --output-dir /mnt/usb/bloud
```

The script stops the host-agent and every container labeled
`io.bloud.managed=true`, checkpoints the SQLite database, writes
`<output-dir>/bloud-<UTC timestamp>.tar.gz` (mode `0600`, it holds
credentials), then starts the containers and the host-agent back. The restart
runs from an `EXIT` trap, so a failed or interrupted archive still leaves the
instance running.

By default the archive is the whole data directory **except** `media/`,
`downloads/`, and the rootless Podman image store under
`.local/share/containers/`. The two media trees are a library, not app state,
and the image store is reproducible from the image tags in the catalog. Pass
`--include-shared` to archive the library too. `--keep N` deletes all but the
newest `N` archives; the default keeps every one.

`--no-stop` skips the stop and takes a crash-consistent archive instead;
`--keep-stopped` restarts the containers but leaves the host-agent down.

## `restore.sh`

```bash
sudo ./restore.sh /var/backups/bloud/bloud-20261006T230552Z.tar.gz
sudo ./restore.sh --latest
sudo ./restore.sh --latest --dry-run
```

The script extracts the archive to a staging directory next to the data
directory first. Only after the extraction has succeeded and the confirmation
has been answered does it stop the host-agent and the containers, replace the
paths the archive carries, and start them back. A truncated or foreign archive
therefore fails without stopping the running instance or changing anything.

Restore is destructive for the paths in the archive. `apps/`, `traefik/`,
`bloud.db`, `secrets.json`, and `host-agent-api-token` are replaced wholesale.
`.local/`, `.cache/`, and `lost+found/` are never touched, and `media/` and
`downloads/` are left alone unless the archive was made with
`--include-shared`. A stale `bloud.db-wal`, `bloud.db-shm`, or
`bloud.db-journal` is removed before the database is put back, because a
database restored under a stale write-ahead log is a corrupt database.

`--dry-run` lists the top-level paths the archive would replace or add, using
only `tar -tzf`, and exits without touching anything.

## `update.sh`

```bash
sudo ./update.sh
sudo ./update.sh --keep 7
sudo ./update.sh --no-backup
```

Update is backup plus upgrade: it runs `backup.sh`, then `install.sh` next to
it, which resolves the newest published `.deb` and installs it with `apt`. If
the upgrade fails, Bloud is already back up and running the previous version.
`--include-shared`, `--keep N`, and `--backup-dir DIR` are forwarded to
`backup.sh`; `--no-backup` skips the backup and is not recommended.

## Why the containers are restarted by name

The scripts start back exactly the containers they stopped. The orchestrator's
periodic pass re-drives a user app whose container is gone, but it deliberately
skips system apps (`traefik`, `authentik`), whose containers are expected to be
kept alive by Podman's own restart policy. An explicit `podman stop` is the one
case where that policy does not apply, so leaving the restart to the
orchestrator would leave the proxy and the identity provider down.
