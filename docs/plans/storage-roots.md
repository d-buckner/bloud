> Status: draft

# Plan: Storage roots: move the media library off the system disk

## Problem

Bloud's media library lives in one fixed place: `$BLOUD_DATA_DIR/media` and
`$BLOUD_DATA_DIR/downloads`. That path is not a setting. It is written into
`apps/*/metadata.yaml` as a template string, and four configurators rebuild the
same join in Go:

| Where | What it hardcodes |
| --- | --- |
| `apps/radarr/metadata.yaml:77` | `{{dataDir}}/media/movies`, `{{dataDir}}/downloads` |
| `apps/sonarr/metadata.yaml:77` | `{{dataDir}}/media/shows`, `{{dataDir}}/downloads` |
| `apps/jellyfin/metadata.yaml:70` | `{{dataDir}}/media/movies`, `{{dataDir}}/media/shows` |
| `apps/navidrome/metadata.yaml:54` | `{{dataDir}}/media/music` |
| `apps/qbittorrent/metadata.yaml:64` | `{{dataDir}}/downloads` |
| `apps/prowlarr/metadata.yaml` | `{{dataDir}}/downloads` |
| `apps/jellyfin/configurator.go:113` | `BloudDataPath` + `media/movies`, `media/shows` |
| `apps/navidrome/configurator.go:86` | `BloudDataPath` + `media/music` |
| `apps/qbittorrent/configurator.go:150` | `BloudDataPath` + `downloads`, `downloads/incomplete` |
| `apps/prowlarr/configurator.go:177` | `BloudDataPath` + `downloads` |

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
at an NFS server.

## The constraint that decides the design

**host-agent runs unprivileged, and `mount(2)` needs `CAP_SYS_ADMIN`.**

The packaged unit (`packaging/bloud-host-agent.service`) has no `User=` and runs
in the `bloud` user's own service manager, with rootless podman. The mount and
unmount syscalls both require `CAP_SYS_ADMIN`
([`mount(2)`](https://man7.org/linux/man-pages/man2/mount.2.html),
[`umount2(2)`](https://www.man7.org/linux/man-pages/man2/umount2.2.html)), and
`mount.nfs` refuses an unprivileged caller outright. Podman cannot do it either:
"Podman cannot mount NFS shares as a normal user, so you will need to add the
NFS share to /etc/fstab"
([kcore.org, 2023-12-13](https://kcore.org/2023-12-13/adventures-with-rootless-containers/)).

So Bloud cannot mount an NFS share on its own. Every design below follows from
that one fact. The chosen answer is a **narrow, root-owned helper binary** that
the host agent calls through `sudo`. The rejected alternatives are in
[Alternatives considered](#alternatives-considered).

## Decision

Four named **storage roots**: `movies`, `shows`, `music`, `downloads`. Each one
is either `local` (the default, under `BLOUD_DATA_DIR`) or `nfs` (a server and an
export). One Settings section edits all four.

```
Settings -> Storage
   movies     local  /var/lib/bloud/media/movies
   shows      local  /var/lib/bloud/media/shows
   music      local  /var/lib/bloud/media/music
   downloads  nfs    nas.local:/volume1/downloads -> /mnt/bloud/downloads
        |
        |  PUT /api/settings/storage  ->  SetStorageRootsIntent
        v
  store settings['storage_roots']  +  live storage.State
        |
        +--> mount planner: which roots need a mount, and are they up?
        +--> template vars {{moviesDir}} {{showsDir}} {{musicDir}} {{downloadsDir}}
        +--> AppState.Libraries  (configurators stop rebuilding the join)
        v
  container specs, app configs, and a node that will not start on a missing mount
```

Six rules carry the design:

1. **The four roots are one value, resolved in one place.** Every consumer (a
   metadata template, a configurator, the mount planner) reads the same
   resolver. Radarr and Jellyfin cannot disagree about where movies live, because
   they cannot each have their own answer.
2. **Defaults reproduce today's paths exactly.** No settings row means
   `$dataDir/media/movies` and `$dataDir/downloads`. The metadata rewrite is
   render-neutral: the rendered spec for an existing install is byte-identical,
   so upgrading recreates no container.
3. **Bloud owns the mount, not `/etc`.** The helper mounts directly. Bloud
   re-derives the mount on every convergence pass, the same way it re-derives
   containers. Nothing is written into `fstab` or `/etc/systemd/system`.
4. **A missing mount blocks a node; it does not corrupt anything.** The mount
   point is root-owned and unwritable while empty, so a dropped mount cannot
   silently fill the system disk. The node stays at `INITIALIZING` with a reason
   that names the root, and the self-healing pass retries it.
5. **The sudo grant is one binary, one path, no shell.** The helper validates
   every argument itself, because it runs as root and must not trust its caller.
6. **Local paths ship first.** Phase 1 makes a root point anywhere on the host,
   which already covers a share the operator mounted in `fstab`. The helper is
   Phase 2.

## Facts this design rests on

Verified against the tree, and against the cited sources, at the time of writing.

- **The uid mapping is the hard part of NFS plus rootless podman.** A container
  uid maps to a subordinate host uid: `0 → <uid>`, `1 → 100000`, so LSIO's
  `PUID=1000` writes as host uid 100999. The repo already documents this
  ([`docs/guides/contributing-apps.md`](../guides/contributing-apps.md), "Writing
  a managed file into a directory the app owns"). The NFS protocol has no concept
  of a user namespace, so the server sees 100999 and knows nothing about the
  mapping
  ([Red Hat, Dan Walsh, 2019-11-25](https://www.redhat.com/en/blog/rootless-podman-nfs)).
  A `chown` or `chmod` that a container root can force on a local filesystem is
  denied by the remote kernel.
- **The arr stack already depends on that mapping.** Sonarr and Radarr import as
  `abc` (a host subuid), so a `0755` host directory is rejected as a root folder,
  and their `PreStart` chmods the library and download trees to `0777`
  ([`docs/plans/media-stack-integration.md`](media-stack-integration.md), finding
  F-1 and post-review fix 1). On an NFS export that chmod only succeeds when the
  export hands ownership to the host-agent user.
- **`mount.nfs` is not in the base image.** Debian's `nfs-common` provides
  `mount.nfs` and `showmount`
  ([packages.debian.org/bookworm/nfs-common](https://packages.debian.org/bookworm/nfs-common)).
  It joins the dependency list in `packaging/nfpm.yaml.tmpl`.
- **`ensureMountDirs` creates every directory mount source** with `MkdirAll` at
  `0755` and only warns on failure
  (`services/host-agent/internal/engine/orchestrator/execution.go:398`). Left
  unchanged, it would create the empty local directory that shadows a share.
- **Settings are a key/value table** (`store.SettingPublicURL` is one key), so a
  new setting needs no schema migration.
- **An address change is the working precedent** for a settings change that must
  reach running apps: `applySetPublicURLIntent` persists, swaps live state, then
  resets the affected nodes so the next pass re-runs their lifecycle
  (`services/host-agent/internal/engine/orchestrator/pipeline_intents.go:26`).
- **Node status carries a message** (`graph.SetActualStatus(id, status, msg)`),
  and `INITIALIZING` is not terminal. `ERROR` is: the media-stack plan records a
  node parked in `ERROR` with "no way back". A missing mount must not use it.
- **The self-healing pass already exists** as the retry driver
  (`orchestrator/selfheal.go`, invariant 8), so a mount that failed because the
  NAS was asleep gets retried without new machinery.

## Design

### 1. The four roots

A new package, `services/host-agent/internal/storage`, owns the vocabulary:

```go
// RootName names one shared library directory.
type RootName string

const (
    RootMovies    RootName = "movies"
    RootShows     RootName = "shows"
    RootMusic     RootName = "music"
    RootDownloads RootName = "downloads"
)

// Root is the desired state of one library directory.
type Root struct {
    Name RootName
    // Mode is "local" or "nfs".
    Mode      string
    // Path is the host path containers see. For a local root the operator
    // may set it; empty means the default under BLOUD_DATA_DIR.
    Path      string
    Server    string // nfs only: host or address of the NFS server
    Export    string // nfs only: absolute export path on that server
    Options   string // nfs only: from the fixed allowlist
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
func (Set) PathFor(RootName) string
func (Set) RemoteRoots() []Root   // the ones that need a mount
```

Precedence per root: the stored setting, then the default under
`BLOUD_DATA_DIR`. `Resolve(settings, dataDir)` is the only function that knows
the defaults, and `dirs` keeps owning the default layout so
`$dataDir/media/movies` is still written down once.

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
   is not one of the five known names. Today an unknown var renders as literal
   text and produces a bind mount named `{{moviesDir}}`.
2. `apps/configtest` asserts that every app which mounts a library uses the root
   var rather than a `{{dataDir}}/media/...` join, so the old shape cannot come
   back.

The rewrite is render-neutral for every existing install: with no setting,
`{{moviesDir}}` resolves to exactly `{{dataDir}}/media/movies`.

### 4. Configurators: `AppState` carries the roots

`pkg/configurator.AppState` gains one field:

```go
// Libraries holds the host path of each shared library directory. A
// configurator uses these instead of joining BloudDataPath by hand.
Libraries Libraries
```

`Libraries` has `Movies`, `Shows`, `Music`, `Downloads`. `BloudDataPath` stays:
it is still the right base for an app's own scratch space. The four joins listed
in [Problem](#problem) become `state.Libraries.Downloads` and friends.

Invariant 14 is not affected: `pkg/configurator` gains a plain struct with four
strings, and pulls in nothing from `internal/`.

### 5. Settings: store, API, intent

One settings key, `store.SettingStorageRoots`, holds a JSON document:

```json
{
  "version": 1,
  "roots": [
    { "name": "downloads", "mode": "nfs",
      "server": "nas.local", "export": "/volume1/downloads" }
  ]
}
```

One document, not four keys: a `PUT` replaces the whole set, so two roots can
never be saved in different generations.

The API mirrors the address setting:

| Route | Behaviour |
| --- | --- |
| `GET /api/settings/storage` | The resolved set, plus live status per root: `mounted`, `path`, `fstype`, `detail`, `defaultPath`, `usable` |
| `PUT /api/settings/storage` | Validates, re-renders canonically, submits `SetStorageRootsIntent`, returns the canonical set so the UI can poll for convergence |
| `POST /api/settings/storage/probe` | Phase 3: attempts a candidate mount in a scratch target, then unmounts it. Answers "will this work" before the operator commits |

Validation runs at the API boundary and again in the orchestrator, for the same
reason `ParsePublicURL` does: the API owes the operator a 400 that names what is
wrong with the string they typed.

`SetStorageRootsIntent` follows the address intent exactly:

1. Parse and canonicalize. Reject a relative path, a `..`, a path inside
   `BLOUD_DATA_DIR/apps`, a server that is not a valid hostname or address, and
   an option outside the allowlist.
2. No-op guard: compare the resolved set, so a save that only reformatted the
   same value restarts nothing.
3. Persist, then swap `storage.State`.
4. Ensure the mounts (below).
5. Reset every node whose rendered spec mounts a root, so `PreStart` rewrites
   configs and the container is recreated against the new source path.

Step 5 is derived, not declared. The orchestrator renders each installed node's
spec and checks whether any mount source sits under a root. No new catalog field
is needed, and an app cannot forget to declare the dependency.

### 6. The mount helper and the sudo grant

A second binary, `bloud-mount`, installed at `/usr/lib/bloud/bloud-mount`,
root-owned, mode `0755`. It is not setuid. The sudoers drop-in
(`packaging/sudoers-bloud`, installed to `/etc/sudoers.d/bloud`) is one line:

```
bloud ALL=(root) NOPASSWD: /usr/lib/bloud/bloud-mount
```

The interface is three verbs:

```
bloud-mount ensure --library movies --server nas.local --export /volume1/media \
                   --target /mnt/bloud/movies [--options <allowlisted>]
bloud-mount remove --target /mnt/bloud/movies
bloud-mount status --target /mnt/bloud/movies        # JSON on stdout
```

The helper runs as root, so it treats its own arguments as hostile input. Its
rules:

- `--target` must be exactly `/mnt/bloud/<name>`, with `<name>` matching
  `^[a-z0-9][a-z0-9-]{0,31}$`. No traversal, no symlink, no other prefix.
- `--export` must be an absolute path with no `..` and no whitespace.
- `--server` must parse as a hostname or an IP address.
- Options come from a fixed allowlist. The default set is
  `vers=4.1,hard,rw,nosuid,nodev,_netdev`. `nosuid` and `nodev` are not
  optional: a mount Bloud created must not be a route to a setuid binary from a
  machine an operator did not choose. `soft` is rejected: a soft mount returns
  `EIO` on timeout, and an arr app then reports a corrupt library rather than a
  waiting one.
- It never calls a shell. It uses `exec.Command` with an argv list, the same
  rule `internal/podman/client.go:94` follows.
- It refuses to mount onto a non-empty directory that is not already a mount
  point. Shadowing someone's existing files is not a failure mode to discover
  later.
- While unmounted, the target is root-owned and mode `0555`. Nothing can write
  into an empty mount point, so a dropped mount cannot fill the system disk.
  Once the export is mounted, the export's own ownership governs.

`/mnt/bloud` sits **outside** `BLOUD_DATA_DIR` on purpose. Backup and restore
never reach it, the orphan sweep under `apps/` cannot delete it, and the local
default roots are untouched by it.

The host agent calls the helper with a timeout, and treats a missing or
refusing helper as a distinct, named condition: "storage management is not
available on this install". Local roots keep working. The dev backends have no
sudo rule, so they run Phase 1 behavior, which is the honest thing for a
workstation.

### 7. Mount lifecycle in the orchestrator

A `storage.Manager` with one entry point the orchestrator calls:

```go
// Ensure makes reality match the desired roots. Idempotent and cheap when
// every root is local.
Ensure(ctx context.Context) []RootStatus
```

`RootStatus` is `Mounted`, `Missing`, `Local`, or `Error` plus a reason string.
It reads `/proc/mounts` first, so a steady-state pass costs one file read.

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

Unmounting is deliberate and conservative: `remove` runs only when an operator
changes a root away from NFS or removes it. It uses a lazy unmount after a
`--timeout`, and a failure is reported, not retried silently, because a busy
mount means a container still holds it.

### 8. A node blocked on a missing mount

`ensureMountDirs` (`execution.go:398`) is the only place that creates a mount
source, so it is the right place to add the guard:

- If a mount source sits under a remote root that is not `Mounted`, do not
  `MkdirAll` it. Leave the tree alone.
- Mark the node `INITIALIZING` with the message
  `waiting for storage root "movies" (nas.local:/volume1/media is not mounted)`.
- Do not create or start the container.

`INITIALIZING` rather than `ERROR` because a NAS that is offline is not a
configuration error, and `ERROR` has no recovery path in this engine. The
self-healing pass re-runs `Ensure`, and the node proceeds the moment the mount
returns. The operator sees a named reason on the tile, not a spinner.

The chmod the arr configurators do today must tolerate `EPERM` on a remote root,
in the same spirit as `managedfile.EnsureWritable`, which leaves a directory the
container already took over alone instead of failing. A library the host agent
cannot chmod is a warning with the export line to fix, not a failed node.

### 9. What the operator has to get right on the server

This is the part that cannot be automated away, so the plan states it and the UI
shows it. Under rootless podman the arr apps write as host uid 100999, and the
NFS server enforces its own rules about that uid. The recommended export hands
ownership to the host-agent user:

```
/volume1/media  192.168.1.0/24(rw,sync,no_subtree_check,all_squash,anonuid=<bloud-uid>,anongid=<bloud-gid>)
```

`all_squash` with an explicit `anonuid` is the recommendation, not an
afterthought. Without it the arr `chmod` fails, and the arrs reject the root
folder as unwritable. The Settings section prints the exact line with the uid
filled in from the running host, because that is the number an operator cannot
guess.

NFS needs no credential, which is one of the reasons it is the first transport.
There is no secret to store, rotate, or leak. Host-level access control is the
model, so the plan is explicit that a share Bloud mounts is reachable by any
host the export allows.

### 10. Dashboard

A fifth settings section, `StorageSettingsSection`, next to Address, AI,
Transcription, and Users. Per root it shows:

- the resolved host path, and a `Local` or `NFS` badge
- for NFS: server, export, and a status dot with the reason text from
  `RootStatus`
- an edit control: mode select, then either a path field or server and export
  fields
- a warning strip when a root is not mounted, naming the apps that are waiting
  on it

The existing `Storage` widget (`web/src/lib/widgets/Storage.svelte`) reports the
data directory through `/api/system/storage`. Phase 3 adds per-root usage, so a
library on a NAS shows the NAS's free space rather than the system disk's.

### 11. Backup, restore, and the docs

- `backup.sh` excludes `media/` and `downloads/` under the data dir already
  ([`docs/operations/backup-restore.md`](../operations/backup-restore.md)).
  Remote roots live under `/mnt/bloud`, outside the archive's reach, so nothing
  can accidentally archive a NAS. The doc gains a sentence saying so.
- `restore.sh` never touches `/mnt/bloud`.
- `AGENTS.md` gains the roots in the data-layout description, and the
  `dirs` package doc gains the pointer to `internal/storage`.
- `docs/guides/contributing-apps.md` gains the rule: an app mounts a library
  through a root var, never through a `{{dataDir}}/media/...` join.

## Security review of the sudo grant

The grant gives the `bloud` user a narrow root capability. That is a real change
to the threat model, and it deserves to be written down rather than shipped
quietly.

What an attacker with the grant can do: mount any export from any NFS server
onto the host, under `/mnt/bloud`, and read what it serves. They cannot choose
the target path, the options, or the filesystem type. They cannot write to any
path outside a mount point Bloud created.

Who already holds that position: anyone who can reach the host-agent admin API
can already install containers with bind mounts, and the `bloud` user already
runs rootless podman. The known open item in
[`docs/operations/tech-debt.md`](../operations/tech-debt.md) is that the loopback
admin bypass is remotely forgeable, and that blocker must be paid first. A
storage mount is a smaller grant than the one that bug already implies.

Controls that make the grant narrow:

1. One binary at one fixed path, root-owned, not writable by `bloud`. That is
   the boundary: if the user could rewrite the helper, the user would be root.
2. The helper validates its own arguments and never invokes a shell.
3. The target prefix is fixed, and mounting onto a non-empty directory is
   refused.
4. `nosuid` and `nodev` are mandatory.
5. Every mount and unmount is logged, and lands in the orchestrator activity
   buffer, so the record says who mounted what and when.
6. Only the admin role can reach the settings route.

The sudoers file is installed by the package, and `docs/operations/packaging.md`
records it. An operator who does not want the grant can remove the drop-in; Bloud
then reports that NFS roots are unavailable and local roots keep working.

## Alternatives considered

**Operator mounts it; Bloud points at a path.** No new privilege, and it is what
Phase 1 delivers anyway. It was rejected as the whole answer because it leaves
the failure mode that hurts most: a share that is not mounted looks like an empty
library, and nothing retries it. It remains the supported path for an operator
who removes the sudo grant.

**A systemd `.mount` and `.automount` unit pair per root.** Automount is the
textbook fix for a missing mount, and `x-systemd.automount` plus `_netdev` and
`nofail` is the documented shape
([`systemd.mount(5)`](https://www.freedesktop.org/software/systemd/man/systemd.mount.html)).
Rejected: it puts desired state in `/etc/systemd/system`, where the reconciler
cannot see it, and Bloud would then have two sources of truth for one setting.
It also needs root to write there anyway, so it buys no privilege reduction.

**fstab edits.** Same objection, with more blast radius: a bad line in `fstab`
can make a machine unbootable.

**A setuid helper.** Rejected outright. A setuid binary is a permanent local
privilege escalation target. `sudo` with a path-pinned rule is auditable and
removable.

**Named podman volumes with the `nfs` driver.** The driver mounts as root at
volume-create time, which is the same privilege problem in a place Bloud does not
control, and it hides the mount point from the planner that needs to know it.

**SMB/CIFS now.** Deferred, not refused. The `mode` field is an open string, so
`cifs` is a later value, not a schema change. It is deferred because it needs a
credential store for the share password, and NFS needs none.

## Phasing

**Phase 1: roots, no privilege.** The `storage` package, the settings key, the
API, the intent, the template vars, the `AppState` field, the metadata rewrite,
the dashboard section with local paths, and the loader plus configtest guards.
Ships value on its own: a library on a second disk, or on a share the operator
mounted in `fstab`.

**Phase 2: NFS.** The helper, the sudoers drop-in, the `nfs-common` dependency,
the mount manager in the pass, the blocked-node guard, the NFS fields and status
in the UI, and the export-line helper text.

**Phase 3: polish.** The probe endpoint, per-root usage in the Storage widget,
per-root usage in diagnostics, and the CIFS design note.

Each phase is a reviewable change on its own, and Phase 1 changes no rendered
output for an existing install.

## Acceptance criteria

Phase 1:

1. A fresh install with no storage setting produces container specs identical to
   today's. Asserted by a golden comparison of the rendered mounts for every
   catalog app that mounts a library.
2. Setting `downloads` to a local path outside `BLOUD_DATA_DIR` makes qBittorrent,
   Prowlarr, Sonarr, and Radarr all mount that path at `/downloads`, and each
   configurator that touched the tree operates on the new path.
3. `GET /api/settings/storage` reports the resolved path and the default path for
   every root, and `PUT` rejects a relative path, a `..`, a path under
   `$BLOUD_DATA_DIR/apps`, and a root inside another root.
4. A `PUT` that changes nothing restarts nothing: the no-op guard is asserted by a
   test that counts container recreations.
5. A `PUT` that moves a root recreates only the nodes that mount it.
6. The catalog loader fails a metadata file with an unknown `{{...}}` var in a
   volume source.
7. `configtest` fails an app that writes a `{{dataDir}}/media/` join.

Phase 2:

8. With the helper present, an NFS root reaches `Mounted`, and the container's
   mount source is the target path under `/mnt/bloud`.
9. With the server unreachable, the root reports `Missing` with a reason, every
   node that mounts it stays at `INITIALIZING` naming that root, and no container
   for those nodes is created.
10. Nothing writes into an unmounted target: the target is root-owned `0555`, and
    a test asserts `MkdirAll` is not called on a path under a missing root.
11. `bloud-mount` refuses a target outside `/mnt/bloud`, a `..` in an export, a
    server string that is not a hostname, an option outside the allowlist, and a
    non-empty non-mountpoint target. Each refusal is a unit test on the argument
    validator, so it runs without root.
12. A root changed back to `local` unmounts the NFS target and re-renders the
    affected nodes onto the local path.
13. A host-agent restart with the mount already present performs no mount call:
    the `/proc/mounts` read is the whole cost.
14. Without the sudo rule, the API reports storage management as unavailable and
    local roots still save and apply.

Phase 3:

15. `POST /api/settings/storage/probe` answers for a bad export in under the
    probe timeout, leaves no mount behind, and names the server's error text.

## Test plan

- **Unit**: the resolver and its defaults, the validator (every rejection), the
  intent's no-op guard, the template render for all four vars, the blocked-node
  decision, and the helper's argument validator as a pure function that needs no
  root.
- **Catalog**: `apps/configtest` gains the root-var assertion; the loader test
  gains the unknown-var case.
- **Integration tier**: a leg that sets `downloads` to a temp directory outside
  the data dir, installs qBittorrent through the real API, and asserts
  behaviorally (through qBittorrent's own API) that a download lands in the new
  directory. This proves the plumbing end to end without a NAS.
- **NFS in CI**: opt-in only. A real NFS server needs `nfs-common` and root in the
  test VM, and a localhost NFS server would test the loopback path rather than the
  interesting one. The plan does not claim coverage it does not have: Phase 2
  ships with a documented manual checklist against a real share, and the
  automated tests cover everything that does not need a mount.
- **e2e**: the dashboard section is exercised by the existing settings specs; a
  new rung asserts a root shows its status and that a bad value is refused in the
  form.

## Files this touches

| Area | Change |
| --- | --- |
| `services/host-agent/internal/storage/` | new: roots, resolve, validate, state, mount client, status. Split so no file nears the 500-line cap |
| `services/host-agent/internal/dirs/dirs.go` | default layout stays authoritative; doc gains the pointer to `storage` |
| `services/host-agent/internal/store/settings.go` | `SettingStorageRoots` |
| `services/host-agent/internal/api/settings_storage.go` | new module file: GET, PUT, probe |
| `services/host-agent/internal/engine/orchestrator/intent.go`, `pipeline.go`, `pipeline_storage.go` | `SetStorageRootsIntent` and its apply |
| `services/host-agent/internal/engine/orchestrator/execution.go` | the mount guard in `ensureMountDirs`, and `Ensure` at the head of a pass |
| `services/host-agent/internal/engine/orchestrator/orchestrator_containers.go` | the four template vars |
| `services/host-agent/internal/catalog/loader.go` | unknown-placeholder rejection |
| `pkg/configurator/interface.go` | `AppState.Libraries` |
| `apps/*/metadata.yaml` | library mounts move to root vars |
| `apps/{jellyfin,navidrome,qbittorrent,prowlarr,radarr,sonarr}/configurator.go` | use `state.Libraries` |
| `cmd/bloud-mount/` | the helper |
| `packaging/` | helper binary, sudoers drop-in, `nfs-common` dependency |
| `services/host-agent/web/src/lib/components/StorageSettingsSection.svelte` | the section |
| `docs/`, `AGENTS.md` | the rules above |

## Out of scope

- SMB/CIFS, and any transport that needs a stored credential.
- Per-app library overrides. One root, one path, every app.
- Quotas, deduplication, and encryption at rest.
- Moving a populated library between roots. Bloud changes where apps look; it
  does not copy the data, and the UI says so before it applies a change.
- Tolerating a mount that drops while a container holds it. The container gets
  I/O errors, and the next pass reports the root as missing.

## Open questions

1. Should `music` and a future `books` root live under one `media` parent by
   default, or is the flat `media/<library>` default good enough to keep?
2. Should a root be allowed to point inside `BLOUD_DATA_DIR` at all, or only
   outside it? The default lives inside it, so "only outside" needs a carve-out.
3. Does the probe endpoint need a rate limit, given that each probe is a mount
   attempt against a host the operator typed?
