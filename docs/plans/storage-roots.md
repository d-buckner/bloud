> Status: draft

# Plan: Storage roots: move the media library off the system disk

## Problem

Bloud's media library lives in one fixed place: `$BLOUD_DATA_DIR/media` and
`$BLOUD_DATA_DIR/downloads`. That path is not a setting. It is written into
`apps/*/metadata.yaml` as a template string, and five more places rebuild the
same join in Go:

| Where | What it hardcodes |
| --- | --- |
| `apps/radarr/` metadata | `{{dataDir}}/media/movies`, `{{dataDir}}/downloads` |
| `apps/sonarr/` metadata | `{{dataDir}}/media/shows`, `{{dataDir}}/downloads` |
| `apps/jellyfin/` metadata | `{{dataDir}}/media/movies`, `{{dataDir}}/media/shows` |
| `apps/navidrome/` metadata | `{{dataDir}}/media/music` |
| `apps/qbittorrent/` metadata | `{{dataDir}}/downloads` |
| `pkg/servarr/pvr.go:170` | `BloudDataPath` + `media/<MediaSubdir>`, + `downloads` |
| `apps/jellyfin/configurator.go:113` | `BloudDataPath` + `media/movies`, `media/shows` |
| `apps/navidrome/configurator.go:86` | `BloudDataPath` + `media/music` |
| `apps/qbittorrent/configurator.go:150` | `BloudDataPath` + `downloads`, `downloads/incomplete` |
| `apps/prowlarr/configurator.go:177` | `BloudDataPath` + `downloads` |

Two of those rows are not the same kind of case, and the difference matters.
Both arrs reach the trees through the shared `pkg/servarr` configurator, so the
per-app join is one field (`PVRApp.MediaSubdir`, a relative path such as
`media/shows`) rather than two copies. Prowlarr creates and opens the downloads
directory but never mounts it: it manages indexers, moves no files, and its
metadata says so in a comment. A configurator can touch a root it does not mount.

The library is the largest thing on a home server, and it is the one thing an
operator most often keeps somewhere else: a NAS, a second disk, or a share that
already holds ten years of rips. Today the only move is to relocate
`/var/lib/bloud/media` by hand and hope nothing else notices.

Three things go wrong when someone does that:

1. Bloud has no idea where the library is, so it cannot tell an operator why an
   import failed.
2. A backup run can follow the moved directory, or miss it, at random.
3. Nothing checks that the path is mounted. An unmounted share reads as an empty
   library, and the first import fills the system disk with a copy of what the
   share should have held.

The ask: make the media and download directories configurable so they can point
at a NAS. The transport is Samba or an S3-compatible object API; it does not have
to be the NFS protocol.

## The two constraints that decide the design

**host-agent runs unprivileged, and no sudo is allowed.** The packaged unit
(`packaging/bloud-host-agent.service`) has no `User=` and runs in the `bloud`
user's own service manager, with rootless podman. `mount(2)` and `umount2(2)`
both require `CAP_SYS_ADMIN`
([`mount(2)`](https://man7.org/linux/man-pages/man2/mount.2.html),
[`umount2(2)`](https://www.man7.org/linux/man-pages/man2/umount2.2.html)), so
the host agent cannot mount anything the ordinary way. Podman cannot help
either: "Podman cannot mount NFS shares as a normal user, so you will need to
add the NFS share to /etc/fstab"
([kcore.org, 2023-12-13](https://kcore.org/2023-12-13/adventures-with-rootless-containers/)).

**NFS cannot be rescued by a user namespace.** An unprivileged user can create a
user namespace and hold `CAP_SYS_ADMIN` inside it, but the kernel grants that
only to filesystems marked `FS_USERNS_MOUNT`, and NFS is deliberately excluded:
"We don't want to set FS_USERNS_MOUNT on NFS, increasing the risk for bugs and
security issues"
([kernel patch thread, 2024-07-24](https://patchew.org/linux/20240724-s._5Fuser._5Fns-fix-v1-1-895d07c94701@kernel.org/)).
A mount made in a private namespace would also be invisible to containers on the
host, so it would not help even if it were allowed.

**FUSE is the exception, and it is the whole door.** FUSE exists to let an
unprivileged process serve a filesystem
([kernel FUSE docs](https://www.kernel.org/doc/html/next/filesystems/fuse/index.html),
"How do non-privileged mounts work?"), and FUSE mounts inside a non-init user
namespace are allowed since Linux 4.18
([kinvolk, 2020-12](https://kinvolk.io/blog/2020/12/improving-kubernetes-and-container-security-with-user-namespaces)).
On a stock Debian box `/dev/fuse` is mode `0666`.

So the design is a **userspace filesystem**: speak the share protocol in Go, and
expose it as a FUSE mount that Bloud creates and supervises itself. The
protocol is Samba or S3 rather than NFS, because that is what a userspace client
reaches well. The rejected options are in
[Alternatives considered](#alternatives-considered).

## Decision

Four named **storage roots**: `movies`, `shows`, `music`, `downloads`. Each one
has a mode: `local` (the default, under `BLOUD_DATA_DIR`), `smb` (a share on a
Samba or Windows server), or `s3` (a bucket on an S3-compatible endpoint). One
Settings section edits all four.

```
Settings -> Storage
   movies     local  /var/lib/bloud/media/movies
   shows      local  /var/lib/bloud/media/shows
   music      s3     minio.local/media
   downloads  smb    nas.local/media-downloads
        |
        |  PUT /api/settings/storage  ->  SetStorageRootsIntent
        v
  store settings['storage_roots']  +  secrets (share and key credentials)
  +  live storage.State
        |
        +--> storage.Manager: mount, unmount, and read status over rclone rc
        +--> template vars {{moviesDir}} {{showsDir}} {{musicDir}} {{downloadsDir}}
        +--> AppState.Libraries  (configurators stop rebuilding the join)
        v
  container specs, app configs, and a node that will not start on a missing root
```

Seven rules carry the design:

1. **The four roots are one value, resolved in one place.** Every consumer (a
   metadata template, a configurator, the mount planner) reads the same
   resolver. Radarr and Jellyfin cannot disagree about where movies live,
   because they cannot each have their own answer.
2. **Defaults reproduce today's paths exactly.** No settings row means
   `$dataDir/media/movies` and `$dataDir/downloads`. The metadata rewrite is
   render-neutral: the rendered spec for an existing install is byte-identical,
   so upgrading recreates no container.
3. **Bloud owns the mount, and nothing outside its own state.** No `fstab` line,
   no `.mount` unit, no sudoers file. The mount is a FUSE mount served by a
   process Bloud starts, so it appears and disappears with the intent that
   asked for it.
4. **A missing root blocks a node; it does not corrupt anything.** The
   reconciler will not start a node whose root is not mounted, and a dead FUSE
   server answers `ENOTCONN` rather than handing out the empty directory
   underneath. rclone also refuses to mount over a non-empty directory unless
   asked, so Bloud never shadows files it did not put there.
5. **One daemon holds every credential.** The share password and the S3 secret
   key reach exactly one process, through a generated config file the host agent
   owns. No credential crosses the API to the dashboard.
6. **Writes to a remote root are buffered locally, with a floor.** A remote
   download area needs a local write cache, and the cache gets a free-space floor
   so it cannot become the disk-full failure this feature exists to prevent.
7. **Local paths ship first.** Phase 1 makes a root point anywhere on the host,
   which already covers a share the operator mounted themselves. The daemon is
   Phase 2.

## Facts this design rests on

Verified against the tree, and against the cited sources, at the time of writing.

- **A FUSE mount is the only filesystem an unprivileged process can mount.** See
  the constraint section above.
- **Without a cache, a FUSE mount of an object store cannot take random writes.**
  "Without the use of `--vfs-cache-mode` this can only write files sequentially,
  it can only seek when reading. This means that many applications won't work
  with their files on an rclone mount"
  ([rclone mount docs](https://rclone.org/commands/rclone_mount/)). With
  `--vfs-cache-mode full`, "all reads and writes are buffered to and from disk"
  and the mount "should support all normal file system operations". qBittorrent
  writes at random offsets while it hashes, so the download root needs `full`.
- **rclone is controllable as a daemon.** `rclone rcd` runs the control server
  alone, and the rc API exposes `mount/mount`, `mount/unmount`, and
  `mount/listmounts` ([rclone.org/rc](https://rclone.org/rc/)). One process can
  therefore serve every root, and the host agent drives it over loopback HTTP.
- **rclone has both transports Bloud needs.** The SMB backend "relies on
  go-smb2 library for communication with SMB protocol"
  ([rclone.org/smb](https://rclone.org/smb/)), and the S3 backend covers MinIO,
  TrueNAS, and the rest of the family. rclone serves NFS but has no NFS client,
  which is one more reason NFS is out.
- **The uid problem becomes a flag.** `rclone mount` takes `--uid`, `--gid`,
  `--file-perms`, `--dir-perms`, and `--umask`, and its uid default is already
  `1000`. Under rootless podman an LSIO app with `PUID=1000` writes as host uid
  100999 given a subordinate range that starts at 100000, and the NFS protocol
  knows nothing about that mapping
  ([Red Hat, Dan Walsh, 2019-11-25](https://www.redhat.com/en/blog/rootless-podman-nfs)).
  A userspace mounter does not care what the server thinks the uid is, because
  it presents one uid to the whole mount.
- **`--allow-other` is the one place a root action remains.** A non-root FUSE
  mount may not pass `allow_other` unless `/etc/fuse.conf` enables
  `user_allow_other` ([ArchWiki, SSHFS](https://wiki.archlinux.org/title/SSHFS)).
  Without it, only the mounting uid can traverse the mount, and the container's
  mapped uids get `EACCES`. The `.deb` postinst already runs as root to create
  the `bloud` user and install a sysctl, so it can uncomment that one line.
- **`ensureMountDirs` creates every directory mount source** with `MkdirAll` at
  `0755` and only warns on failure
  (`services/host-agent/internal/engine/orchestrator/execution.go:398`). Left
  unchanged, it would create the empty local directory that shadows a share.
- **Settings are a key/value table** (`store.SettingPublicURL` is one key), so a
  new setting needs no schema migration. Credentials do not go there: they go
  through the secrets provider, the same route every app credential takes.
- **An address change is the working precedent** for a settings change that must
  reach running apps: `applySetPublicURLIntent` persists, swaps live state, then
  resets the affected nodes so the next pass re-runs their lifecycle
  (`services/host-agent/internal/engine/orchestrator/pipeline_intents.go:26`).
- **Node status carries a message** (`graph.SetActualStatus(id, status, msg)`),
  and `INITIALIZING` is not terminal. `ERROR` is: the media-stack plan records a
  node parked in `ERROR` with "no way back". A missing root must not use it.
- **The self-healing pass already exists** as the retry driver
  (`orchestrator/selfheal.go`, invariant 8), so a root that failed because the
  NAS was asleep gets retried without new machinery.

## Design

### 1. The four roots

A new package, `services/host-agent/internal/storage`, owns the desired state.
Its root names are the `configurator.LibraryName` values from section 4, so the
host agent, `pkg/servarr`, and the catalog templates all name a library with the
same four strings:

```go
// Root is the desired state of one library directory.
type Root struct {
    Name configurator.LibraryName
    // Mode is "local", "smb", or "s3".
    Mode string
    // Path is the host path containers see. For a local root the operator
    // may set it; empty means the default under BLOUD_DATA_DIR.
    Path string
    // Remote fields, per mode.
    Server string // smb: host; s3: endpoint host and port
    Share  string // smb: share name
    Bucket string // s3: bucket
    Prefix string // s3: key prefix inside the bucket, optional
    ReadOnly bool  // both: refuse writes at the mount, not at the app
}
```

The set is a fixed table, not an open-ended list. A new library (a `books` root
for a future Kavita entry) is one entry in that table plus its metadata line,
which is the same shape as every other catalog-wide derivation
([`docs/plans/app-add-tax.md`](app-add-tax.md)).

`downloads` is a root like the others even though it is not a library, because
qBittorrent, Prowlarr, and both arrs mount it and must agree on it.

### 2. Resolution and precedence

`storage.State` holds the live set, mirroring `hostset.State`:

```go
type State struct{ /* mutex + resolved map */ }

func (s *State) Get() Set
func (s *State) Set(Set)
func (Set) PathFor(configurator.LibraryName) string
func (Set) RemoteRoots() []Root // the ones that need a mount
```

Precedence per root: the stored setting, then the default under
`BLOUD_DATA_DIR`. `Resolve(settings, dataDir)` is the only function that knows
the defaults, and `dirs` keeps owning the default layout so
`$dataDir/media/movies` is still written down once.

A remote root resolves to a mount point under `/mnt/bloud/<name>`. That path
sits **outside** `BLOUD_DATA_DIR` on purpose: backup and restore never reach it,
the orphan sweep under `apps/` cannot delete it, and the local default roots are
untouched by it. The write cache deliberately sits the other way, inside
`BLOUD_DATA_DIR`, because it is Bloud state on local disk.

`Set` renders the template vars and answers the planner's questions. Nothing
else computes a path.

### 3. Catalog: template vars replace the hardcoded join

`ContainerSpecFromDef` already renders `{{dataDir}}` and `{{appDataDir}}`
(`orchestrator_containers.go:220`). Add the four roots to the same render:

```yaml
# apps/radarr/metadata.yaml, after
volumes:
  - source: "{{moviesDir}}"
    destination: /data/movies
  - source: "{{downloadsDir}}"
    destination: /downloads
```

Two guards make a typo impossible to ship:

1. The catalog loader rejects a `{{...}}` placeholder in `volumes[].source` that
   is not one of the names a volume source may use: `dataDir`, `appDataDir`, and
   the four roots. Today an unknown var renders as literal text and produces a
   bind mount named `{{moviesDir}}`.
2. `apps/configtest` asserts that every app which mounts a library uses the root
   var rather than a `{{dataDir}}/media/...` join, so the old shape cannot come
   back.

The rewrite is render-neutral for every existing install: with no setting,
`{{moviesDir}}` resolves to exactly `{{dataDir}}/media/movies`.

### 4. Configurators: `AppState` carries the roots

The vocabulary lives in `pkg/configurator`, not in `internal/storage`, because
`pkg/servarr` has to name a library and invariant 14 forbids a `pkg/` import of
`internal/`. One enum, one place:

```go
// LibraryName names one shared library directory.
type LibraryName string

const (
    LibraryMovies    LibraryName = "movies"
    LibraryShows     LibraryName = "shows"
    LibraryMusic     LibraryName = "music"
    LibraryDownloads LibraryName = "downloads"
)

// Libraries maps each library name to its resolved host path.
type Libraries map[LibraryName]string
```

`pkg/configurator.AppState` gains one field:

```go
// Libraries holds the host path of each shared library directory. A
// configurator uses these instead of joining BloudDataPath by hand.
Libraries Libraries
```

`BloudDataPath` stays: it is still the right base for an app's own scratch space.
The joins listed in [Problem](#problem) become
`state.Libraries[LibraryDownloads]` and friends, and `PVRApp.MediaSubdir` (a
relative path) becomes `PVRApp.MediaLibrary` (a `LibraryName`).
`internal/storage` uses those same four strings as its root names, so there is
one vocabulary and no mapping table between the two.

Invariant 14 still holds: `pkg/configurator` gains a string type and a map of
strings, and pulls in nothing from `internal/`.

### 5. Settings: store, API, intent

One settings key, `store.SettingStorageRoots`, holds a JSON document:

```json
{
  "version": 1,
  "roots": [
    { "name": "downloads", "mode": "smb",
      "server": "nas.local", "share": "media-downloads", "readOnly": false }
  ]
}
```

One document, not four keys: a `PUT` replaces the whole set, so two roots can
never be saved in different generations. **No credential appears in this
document.** An SMB password and an S3 secret key live in the secrets store under
the storage scope, and the settings response reports only that a credential is
set, never its value.

The API mirrors the address setting:

| Route | Behaviour |
| --- | --- |
| `GET /api/settings/storage` | The resolved set, plus live status per root: `mounted`, `path`, `detail`, `defaultPath`, `cachePath`, `cacheFree` |
| `PUT /api/settings/storage` | Validates, re-renders canonically, submits `SetStorageRootsIntent`, returns the canonical set so the UI can poll for convergence |
| `POST /api/settings/storage/probe` | Phase 3: mounts a candidate in a scratch directory, reads one directory, unmounts. Answers "will this work" before the operator commits |

Validation runs at the API boundary and again in the orchestrator, for the same
reason `ParsePublicURL` does: the API owes the operator a 400 that names what is
wrong with the string they typed.

`SetStorageRootsIntent` follows the address intent exactly:

1. Parse and canonicalize. Reject a relative path, a `..`, a path inside
   `BLOUD_DATA_DIR/apps`, a server that is not a valid hostname or address, an
   empty share or bucket, and an `s3` root with a prefix that escapes the bucket.
2. No-op guard: compare the resolved set, so a save that only reformatted the
   same value restarts nothing.
3. Persist the settings row and the credential, then swap `storage.State`.
4. Ensure the mounts (section 7).
5. Reset every node whose rendered spec mounts a root, so `PreStart` rewrites
   configs and the container is recreated against the new source path.

Step 5 is derived, not declared. The orchestrator renders each installed node's
spec and checks whether any mount source sits under a root. No new catalog field
is needed, and an app cannot forget to declare the dependency.

The derivation reads the mount, not the configurator. Prowlarr opens the
downloads directory in `PreStart` but mounts nothing, so moving `downloads` does
not reset Prowlarr. That is the right answer: its config holds no path, and a
restart would realign nothing. It also means a configurator that writes into a
remote root has to cope with that root being absent, which is what
[section 8](#8-a-node-blocked-on-a-missing-root) covers.

### 6. The storage daemon

One process serves every remote root: `rclone rcd`, the rclone control server
with no mount of its own. The host agent owns its configuration and its
lifecycle, and talks to it over loopback HTTP.

```
/usr/lib/bloud/rclone rcd
    --rc-addr 127.0.0.1:0            # ephemeral port, loopback only
    --rc-user bloud --rc-pass <token>
    --config $BLOUD_DATA_DIR/storage/rclone.conf
```

| Concern | Decision |
| --- | --- |
| Supervision | A **user** unit, `bloud-storage.service`, with `Restart=always`. A user unit needs no privilege, and keeping the daemon out of the host-agent unit means a host-agent restart does not drop every mount |
| Config file | Generated by the host agent from the settings row plus the secrets store, mode `0600`, never hand-edited, rewritten on every apply |
| Control | `mount/mount` per remote root, `mount/unmount` when a root changes mode, `mount/listmounts` for status |
| Auth | Loopback bind, a generated rc token, and the token is rotated with the rest of the secrets |
| Version | Pinned, shipped in the package next to the host-agent binary, and covered by the same pin discipline as container images |
| FUSE options | `--allow-other`, `--uid`/`--gid` set to the host-agent user, `--dir-perms 0777`, `--file-perms 0666` |
| Never set | `--allow-non-empty`. Refusing to mount over a non-empty directory is the anti-shadowing guard, and Bloud does not turn it off |

The pinned-version choice is deliberate. rclone is a large third-party binary in
the supply chain, and the repository already refuses floating versions for
container images for the same reason: a rolling artifact makes a regression
unreviewable and lands on every install at once. The pin check in
`scripts/pinned-images.mjs` grows a companion entry for the rclone version
string, so a bump is a reviewed change rather than a surprise.

The daemon is a distinct, named dependency. When the binary is absent (a source
checkout, a dev backend, or a machine where the operator removed it), the API
reports that remote roots are unavailable and local roots keep working. That is
the honest degradation, and it keeps Phase 1 usable everywhere.

### 7. Mount lifecycle in the orchestrator

A `storage.Manager` with one entry point the orchestrator calls:

```go
// Ensure makes reality match the desired roots. Idempotent and cheap when
// every root is local.
Ensure(ctx context.Context) []RootStatus
```

`RootStatus` is `Mounted`, `Missing`, `Local`, or `Error` plus a reason string.
It reads `/proc/mounts` first, so a steady-state pass costs one file read, and
it only asks the daemon when the file and the desired set disagree.

It runs at three moments, all already in the architecture:

1. At the start of a convergence pass, before the graph levels walk. The
   bootstrap gate (invariant 5) stays closed until the first pass returns, so an
   app never starts against a library that is not there.
2. From `applySetStorageRootsIntent`.
3. On the self-healing timer, which is a convergence pass, so it needs no new
   hook. This is what recovers a NAS that was asleep when the box booted.

Bloud does not write a `.mount` unit. A unit in `/etc/systemd/system` is state
Bloud does not re-derive, and the whole point of the reconciler is that reality
is re-derived from intent every pass. The rejected option is recorded in
[Alternatives considered](#alternatives-considered).

One extra case is new to a userspace mount: a FUSE mount can be present in
`/proc/mounts` while the process behind it is gone. `Ensure` treats that as
`Error`, restarts the user unit, and reports the reason. A stale mount that
answers `ENOTCONN` to every call is worse than no mount, because the reconciler
would read it as success.

Unmounting is deliberate and conservative: it runs only when an operator changes
a root away from a remote mode or removes it. A failure is reported, not retried
silently, because a busy mount means a container still holds files open, and the
operator has to stop those apps first.

### 8. A node blocked on a missing root

`ensureMountDirs` (`execution.go:398`) is the only place that creates a mount
source, so it is the right place to add the guard:

- If a mount source sits under a remote root that is not `Mounted`, do not
  `MkdirAll` it. Leave the tree alone.
- Mark the node `INITIALIZING` with the message
  `waiting for storage root "downloads" (nas.local/media-downloads is not mounted)`.
- Do not create or start the container.

`INITIALIZING` rather than `ERROR` because a NAS that is offline is not a
configuration error, and `ERROR` has no recovery path in this engine. The
self-healing pass re-runs `Ensure`, and the node proceeds the moment the mount
returns. The operator sees a named reason on the tile, not a spinner.

The chmod the arr configurators do today must tolerate `EPERM` on a remote root,
in the same spirit as `managedfile.EnsureWritable`, which leaves a directory the
container already took over alone instead of failing. A library the host agent
cannot chmod is a warning, not a failed node. Today `pkg/servarr/pvr.go:174`
turns a failed `MkdirAll` into a `PreStart` error, and a `PreStart` error is
terminal for the node. So the guard runs before that call: when the root is not
mounted the node waits, and when the root is mounted but unwritable it warns.

### 9. The write cache

A remote root that takes writes needs a local cache, and this is the part of the
design most likely to bite, so it gets its own section.

- **A read-only root needs no cache.** A `movies` root that only Jellyfin reads
  runs with `--vfs-cache-mode off`, `--read-only`, and a large
  `--vfs-read-chunk-size`. Streaming works, and nothing is buffered on disk.
- **The download root needs `full`.** qBittorrent writes at random offsets while
  it hashes, and without a write cache that is corrupt data, not slow data.
- **The cache is Bloud-managed, at `$BLOUD_DATA_DIR/storage-cache/<root>`.**
  rclone keeps sparse files there in `full` mode, so a cache file reports a size
  it has not actually used.
- **The cache gets a floor.** `--vfs-cache-min-free-space` comes from the
  settings row, and the default leaves headroom on the disk that holds
  `BLOUD_DATA_DIR`. Below the floor, writes fail with `ENOSPC` at the mount,
  which is a visible error, rather than filling the system disk, which is the
  exact failure this feature exists to prevent.
- **The UI says it out loud.** A remote download root shows the cache path, the
  floor, and the current free space, with a warning when the local disk is
  smaller than the operator's usual download volume. The honest advice is: keep
  `downloads` local when the system disk is small, and put the library on the
  NAS.

An `s3` root carries one more cost the operator must know about: an object store
has no rename. A Radarr rename of a 40 GB file is a copy and a delete, and there
is no flag that makes it cheap. The Settings section states this for an `s3` root
that is not read-only, rather than letting the operator discover it as a stalled
import.

### 10. What the operator has to get right

Far less than an NFS export, because the mounter presents one uid and the
protocol carries a real credential.

- **SMB:** a share name, a username, and a password, plus a domain field for a
  Windows or AD box. The share must be writable by that account for a
  read-write root. Nothing on the server names a uid range.
- **S3:** an endpoint, a bucket, an access key and a secret, and a region.
  Path-style addressing is the default, because a MinIO or TrueNAS endpoint
  rarely answers virtual-host style on a LAN IP.
- **The one host requirement:** `/etc/fuse.conf` must enable `user_allow_other`
  so the daemon can pass `--allow-other`. The package postinst writes it. The
  dashboard shows it as a prerequisite with the exact line, and reports the
  current state rather than failing with `EACCES` inside an app.

The uid story that made NFS painful is now a mount flag: `--uid` and `--gid`
decide who owns everything under the mount, and `--file-perms` and `--dir-perms`
decide the mode. The arrs' "Folder is not writable by user 'abc'" failure becomes
a setting Bloud already controls.

### 11. Dashboard

A fifth settings section, `StorageSettingsSection`, next to Address, AI,
Transcription, and Users. Per root it shows:

- the resolved host path, and a `Local`, `SMB`, or `S3` badge
- for a remote root: server, share or bucket, and a status dot with the reason
  text from `RootStatus`
- an edit control: mode select, then either a path field or the fields the mode
  needs, plus a password field that reads back "set" rather than the value
- a cache line for a writable remote root: path, floor, free space
- a warning strip when a root is not mounted, naming the apps that are waiting
  on it

The existing `Storage` widget reports the data directory through
`/api/system/storage`. Phase 3 adds per-root usage, so a library on a NAS shows
the NAS's free space rather than the system disk's.

### 12. Backup, restore, and the docs

- `backup.sh` excludes `media/` and `downloads/` under the data dir already
  ([`docs/operations/backup-restore.md`](../operations/backup-restore.md)).
  Remote mount points live under `/mnt/bloud`, outside the archive's reach, so
  nothing can accidentally archive a NAS. The write cache lives inside the data
  directory and **must** be excluded: it is a local copy of remote bytes, and it
  can be many gigabytes. That exclusion is a change to the script, not a
  documentation note.
- `restore.sh` never touches `/mnt/bloud`.
- `AGENTS.md` gains the roots in the data-layout description, and the `dirs`
  package doc gains the pointer to `internal/storage`.
- `docs/guides/contributing-apps.md` gains the rule: an app mounts a library
  through a root var, never through a `{{dataDir}}/media/...` join.
- `docs/operations/packaging.md` gains the rclone binary, the user unit, and the
  `fuse.conf` line.

## Security review

Nothing here runs as root, so the review is about credentials and about a
filesystem Bloud serves to itself.

**Credentials.** An SMB password and an S3 secret key are long-lived and
high-value. They live in the secrets store, never in the settings row, never in a
GET response, and never in a log line. The generated `rclone.conf` is mode
`0600` inside `BLOUD_DATA_DIR`, which is already inside the backup archive, so a
rotated credential is a rewritten file and a new secret, not a stale copy in an
old archive.

**The rc endpoint.** It can mount anything the config can reach, so it binds to
loopback, requires the generated token, and is never proxied. The port is
ephemeral and discovered from the daemon's own startup output rather than fixed,
so a second process cannot pre-bind it.

**`allow_other` is a real widening.** It lets every uid on the host traverse the
mount, which is what the containers need and what a private mount would not
allow. The mitigation is that the mount is not writable by accident: modes come
from `--file-perms` and `--dir-perms`, and a read-only root is mounted
`--read-only` rather than left to permission luck.

**A mounted filesystem is an input.** An SMB server or an S3 endpoint on the LAN
can return any tree it likes, including names that surprise. The mount point is
confined to `/mnt/bloud/<name>`, rclone refuses a non-empty target, and no Bloud
path is built by concatenating a name that came from the remote.

**Who can reach this surface.** Only the admin role, through the settings route.
The open item in [`docs/operations/tech-debt.md`](../operations/tech-debt.md) is
that the loopback admin bypass is remotely forgeable, and that matters here: while
a forged header grants admin, it also grants "point the library at a server I
control" and "read the credential you just stored". **Phase 2 should not ship
before that debt is paid.** Phase 1 needs no credential and no privilege, so it
carries none of this.

## Alternatives considered

**NFS.** Rejected by the kernel, not by taste: NFS is not `FS_USERNS_MOUNT`, so an
unprivileged process cannot mount it in any namespace where the result would be
visible to containers. A sudoers rule would work, and was rejected as a product
decision: a persistent root capability is a larger change to the threat model
than a userspace mounter.

**A sudoers rule for a root-owned mount helper.** The previous shape of this plan.
It gives automatic recovery and the kernel client's performance, and it costs a
permanent root grant. Rejected on the no-sudo constraint.

**Operator mounts it; Bloud points at a path.** No new privilege, and Phase 1
delivers it anyway. Rejected as the whole answer because it leaves the failure
mode that hurts most: a share that is not mounted looks like an empty library,
and nothing retries it. It stays the supported path for an operator who does not
want the daemon.

**A systemd `.mount` and `.automount` unit pair per root.** Automount is the
textbook fix for a missing mount, and `x-systemd.automount` with `_netdev` and
`nofail` is the documented shape
([`systemd.mount(5)`](https://www.freedesktop.org/software/systemd/man/systemd.mount.html)).
Rejected: it puts desired state in `/etc/systemd/system`, where the reconciler
cannot see it, and it needs root to write there.

**fstab edits.** Same objection, with more blast radius: a bad line can make a
machine unbootable.

**An in-house SMB mounter: `go-smb2` behind `go-fuse`.** `hirochachacha/go-smb2`
is a pure-Go SMB2/3 client with a `Mount` API
([pkg.go.dev](https://pkg.go.dev/github.com/hirochachacha/go-smb2)), and
`hanwen/go-fuse/v2` can call `mount(2)` directly instead of shelling out to
`fusermount` ([go-fuse #423](https://github.com/hanwen/go-fuse/issues/423)).
This is a few hundred lines of glue and removes a large binary from the supply
chain. Deferred, not refused: rclone buys S3, WebDAV, and FTP on day one, and the
interface in this plan is a mount and unmount call, so the swap is contained.

**A setuid helper.** Rejected outright. A setuid binary is a permanent local
privilege escalation target.

**Named podman volumes with the `nfs` driver.** The driver mounts as root at
volume-create time, which is the privilege problem again, and it hides the mount
point from the planner that needs to know it.

## Phasing

**Phase 1: roots, no privilege.** The `storage` package, the settings key, the
API, the intent, the template vars, the `AppState` field, the metadata rewrite,
the dashboard section with local paths, and the loader plus configtest guards.
Ships value on its own: a library on a second disk, or on a share the operator
mounted themselves.

**Phase 2: remote roots.** The pinned rclone binary, `bloud-storage.service`,
the `storage.Manager` over rc, the generated `rclone.conf`, the credential
storage, the SMB and S3 fields and status in the UI, the write cache with its
floor, and the `fuse.conf` line in the postinst.

**Phase 3: polish.** The probe endpoint, per-root usage in the Storage widget and
in diagnostics, the rclone pin check, and a look at whether the in-house SMB
mounter earns its keep.

Each phase is a reviewable change on its own, and Phase 1 changes no rendered
output for an existing install.

## Acceptance criteria

Phase 1:

1. A fresh install with no storage setting produces container specs identical to
   today's. Asserted by a golden comparison of the rendered mounts for every
   catalog app that mounts a library.
2. Setting `downloads` to a local path outside `BLOUD_DATA_DIR` makes
   qBittorrent, Sonarr, and Radarr all mount that path at `/downloads`; Prowlarr,
   which mounts nothing but opens that directory, operates on the new path.
3. `GET /api/settings/storage` reports the resolved path and the default path
   for every root, and `PUT` rejects a relative path, a `..`, a path under
   `$BLOUD_DATA_DIR/apps`, and a root inside another root.
4. A `PUT` that changes nothing restarts nothing: the no-op guard is asserted by
   a test that counts container recreations.
5. A `PUT` that moves a root recreates only the nodes that mount it.
6. The catalog loader fails a metadata file with an unknown `{{...}}` var in a
   volume source.
7. `configtest` fails an app that writes a `{{dataDir}}/media/` join.

Phase 2:

8. With the daemon present, an SMB root reaches `Mounted`, and the container's
   mount source is the mount point under `/mnt/bloud`.
9. With the server unreachable, the root reports `Missing` with a reason, every
   node that mounts it stays at `INITIALIZING` naming that root, and no container
   for those nodes is created.
10. Nothing writes into an unmounted mount point: a test asserts `MkdirAll` is
    not called on a path under a missing root.
11. An `s3` root that is not read-only is refused, or warned, until the operator
    acknowledges the rename cost. A read-only root mounts with `--read-only` and
    `--vfs-cache-mode off`.
12. A writable remote root gets `--vfs-cache-mode full`, a cache directory under
    `$BLOUD_DATA_DIR/storage-cache`, and a `--vfs-cache-min-free-space` floor
    derived from the settings row.
13. A FUSE entry present in `/proc/mounts` with no daemon behind it reports
    `Error`, restarts the user unit, and converges on the next pass.
14. A root changed back to `local` unmounts, re-renders the affected nodes onto
    the local path, and leaves no orphan mount.
15. `GET /api/settings/storage` never returns a credential value, and the
    generated `rclone.conf` is mode `0600`.
16. Without the rclone binary, the API reports remote roots as unavailable and
    local roots still save and apply.
17. `backup.sh` excludes the write cache directory.

Phase 3:

18. `POST /api/settings/storage/probe` answers for a bad share within the probe
    timeout, leaves no mount behind, and names the server's error text.

## Test plan

- **Unit**: the resolver and its defaults, the validator (every rejection), the
  intent's no-op guard, the template render for all four vars, the blocked-node
  decision, the rc client against a stub HTTP server, and the generated
  `rclone.conf` (mode, quoting, and the absence of a credential in any log line).
- **Catalog**: `apps/configtest` gains the root-var assertion; the loader test
  gains the unknown-var case.
- **Integration tier, S3**: `rclone serve s3` runs a real Signature Version 4
  endpoint with no root
  ([rclone serve s3](https://rclone.org/commands/rclone_serve_s3/)). A leg sets
  `downloads` to an `s3` root backed by that server, installs qBittorrent through
  the real API, and asserts behaviorally that a download lands in the bucket.
  This is a real end-to-end test of the remote path, not a mock.
- **Integration tier, local**: a leg that sets `downloads` to a temp directory
  outside the data dir and proves the plumbing reaches the app.
- **SMB**: manual. rclone has no SMB server, so the SMB leg needs a real Samba
  host. Phase 2 ships with a documented checklist against one, and the automated
  tests cover everything that does not need a server.
- **e2e**: the dashboard section is exercised by the existing settings specs; a
  new rung asserts a root shows its status and that a bad value is refused in the
  form.

## Files this touches

| Area | Change |
| --- | --- |
| `services/host-agent/internal/storage/` | new: roots, resolve, validate, state, rc client, status. Split so no file nears the 500-line cap |
| `services/host-agent/internal/dirs/dirs.go` | default layout stays authoritative; doc gains the pointer to `storage` |
| `services/host-agent/internal/store/settings.go` | `SettingStorageRoots` |
| `services/host-agent/internal/api/settings_storage.go` | new module file: GET, PUT, probe |
| `services/host-agent/internal/api/settings_module.go` | registers the routes on the admin router |
| `services/host-agent/internal/engine/orchestrator/intent.go`, `pipeline.go`, `pipeline_storage.go` | `SetStorageRootsIntent` and its apply |
| `services/host-agent/internal/engine/orchestrator/execution.go` | the mount guard in `ensureMountDirs`, and `Ensure` at the head of a pass |
| `services/host-agent/internal/engine/orchestrator/orchestrator_containers.go` | the four template vars |
| `services/host-agent/internal/catalog/loader.go` | unknown-placeholder rejection |
| `pkg/configurator/interface.go` | `LibraryName`, `Libraries`, `AppState.Libraries` |
| `pkg/servarr/pvr.go` | `MediaSubdir` becomes `MediaLibrary`; the joins read `state.Libraries` |
| `apps/*/metadata.yaml` | library mounts move to root vars |
| `apps/{jellyfin,navidrome,qbittorrent,prowlarr}/configurator.go` | use `state.Libraries` |
| `apps/configtest/configtest.go` | the root-var assertion |
| `packaging/` | pinned rclone binary, `bloud-storage.service` user unit, the `fuse.conf` line in the postinst |
| `scripts/pinned-images.mjs` | the rclone version pin check |
| `services/host-agent/web/src/lib/components/StorageSettingsSection.svelte` | the section |
| `services/host-agent/web/src/routes/settings/+page.svelte` | mounts the section |
| `backup.sh` | exclude the write cache |
| `docs/`, `AGENTS.md` | the rules above |

## Out of scope

- NFS, and any transport that needs a kernel mount.
- iSCSI, and anything that presents a block device rather than a filesystem.
- Per-app library overrides. One root, one path, every app.
- Quotas, deduplication, and encryption at rest.
- Moving a populated library between roots. Bloud changes where apps look; it
  does not copy the data, and the UI says so before it saves.
