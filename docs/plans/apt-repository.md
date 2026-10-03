> Status: draft

# Plan: Signed apt repository (`apt update && apt upgrade`)

**Last updated:** 2026-10-03

## Goal

A Bloud install should update the same way every other Debian package does:

```bash
# once
curl -fsSL https://d-buckner.github.io/bloud/install.sh | sudo sh

# forever after
sudo apt update && sudo apt upgrade
```

Today the `.deb` exists, but it is distributed as immutable GitHub pre-releases
resolved at install time by a hand-written `install.sh`. There is no
repository, no `apt update`, and the package is unsigned. This plan replaces
that path with a signed apt repository served from GitHub Pages.

## The unlock: a rolling index, not a rolling asset

The repository has GitHub release immutability enabled, which is what killed a
rolling fixed-name asset (an asset cannot be overwritten, and `install.sh`
therefore resolves the newest of many `deb-<UTC timestamp>` releases). A Debian
repository does not need a rolling asset. It needs a rolling **index** over an
append-only pool:

```
debian/
  pool/main/b/bloud/bloud_<version>_amd64.deb   # append per push, never rewrite
  dists/stable/main/binary-amd64/Packages{,.gz} # regenerated index
  dists/stable/Release
  dists/stable/InRelease                        # clearsigned by the repo key
  dists/stable/Release.gpg                      # detached signature for older apt
```

Each push appends the new `.deb`, regenerates the index, re-signs it, and
publishes the branch. GitHub Pages serves it from a stable HTTPS origin, and
release immutability never applies to a git branch. This is the whole trick.

## Decisions

- **Hosting:** GitHub Pages on the main repo, apt tree under `debian/`, leaving
  the Pages root free. Source line:

  ```
  deb [signed-by=/etc/apt/keyrings/bloud-archive-keyring.gpg] https://d-buckner.github.io/bloud/debian stable main
  ```

- **Versioning:** semver from tags. A `vX.Y.Z` tag on main publishes `X.Y.Z`;
  N commits after it publish `X.Y.Z+N`; with no `v*` tag yet (the current
  state) everything publishes `0.0.0+<commit-count>`. `~dirty` marks a dirty
  tree. This is monotonic under Debian's rules: within one upstream version a
  higher commit count wins, and the next tag always beats every build that
  preceded it.
- **Old path retires.** The `deb-<UTC timestamp>` release workflow and the
  release-resolution half of `install.sh` are removed. The repo is the only
  install and update path. The raw `.deb` still uploads as a workflow artifact
  for inspection.

## Versioning

`cli/package.go`'s `packageVersion` today does
`git describe --tags --always --dirty`, strips a leading `v`, and prefixes
`0.0.0+` when the result does not start with a digit. The output
(`0.0.0+deb-20261003-041521-1-geea3cca`) is Debian-*valid* but its ordering
across pushes is accidental.

Replace it with a scheme that is deliberately monotonic:

1. `git describe --tags --match 'v[0-9]*' --dirty`:
   - exact tag `v1.2.3` → `1.2.3`
   - `v1.2.3-5-g<sha>` → `1.2.3+5`
   - `-dirty` → append `~dirty` (sorts below the clean build)
2. No `v*` tag matches (today's state) → `0.0.0+` plus
   `git rev-list --count HEAD`.

`1.2.3+5` beats `1.2.3+4` and loses to `1.2.4`, because Debian compares the
digit run `5` numerically against `4`, and the upstream run `4` beats `3`.
The existing `deb-*`, `iso-*`, `latest`, and `vm-images` tags are all ignored
by the `v[0-9]*` match, so they neither gate nor pollute the version.

The cadence is a note, not machinery: pushing `vX.Y.Z` on a release day names
that build; every push in between is `X.Y.Z+N` or `0.0.0+N`. No CHANGELOG, no
release-please, no enforced bump.

## Signing

The repo key is the trust root. The user installs it once; `apt` then verifies
`InRelease` → `Packages` → `.deb` checksum on every update, which is strictly
stronger than today's `install.sh` checksum check over the same TLS origin.

- Generate a dedicated key once (RSA 4096 for widest `apt` compatibility, or
  ed25519 on apt 2.2+): `Bloud Archive <...>`.
- Commit the public key at `packaging/bloud-archive-keyring.gpg` so it is
  versioned, and also serve it from the Pages origin for the bootstrap.
- Keep the private key offline; export a CI-only signing subkey (or the primary
  key encrypted with a passphrase) into a GitHub Actions secret.
- The publish job imports the secret, signs `Release` into `InRelease`
  (`gpg --clearsign`), and writes the detached `Release.gpg`.

Key rotation and revocation are deferred but noted: the keyring is a file in
the repo, so rotating means committing a new keyring and publishing a new
`InRelease`; a revocation path needs a follow-up.

## CI: build and publish

The existing `release.yml` keeps its build steps (Go, Node, `./bloud package`)
and swaps the publish step:

1. Build the `.deb` (already working).
2. Check out the `gh-pages` branch (or an `apt` branch deployed via Pages).
3. Append the `.deb` to `debian/pool/main/b/bloud/`.
4. Apply retention: keep the newest K versions per architecture, drop the rest
   from the pool and the index (default K = 10).
5. `dpkg-scanpackages` (or `apt-ftparchive packages`) into
   `dists/stable/main/binary-amd64/Packages`, then gzip it.
6. `apt-ftparchive release` the suite, then `gpg --clearsign` into `InRelease`
   and `--detach-sign` into `Release.gpg`.
7. Commit and push (or `actions/deploy-pages`).

`apt-ftparchive` and `dpkg-scanpackages` come from `apt-utils`/`dpkg-dev`,
which the ubuntu-24.04 runner already has. No new tool dependency.

`deb-verify.yml` is unchanged: it verifies the `.deb`'s dependency model, which
is independent of how the `.deb` is distributed.

## Bootstrap

`install.sh` shrinks to the one-time bootstrap, keeping its "nothing that can
drift from the package" rule:

1. Download `bloud-archive-keyring.gpg` and install it to
   `/etc/apt/keyrings/` (dereferencing the key to a `.gpg` keyring).
2. Write `/etc/apt/sources.list.d/bloud.list` with the source line above.
3. `apt-get update && apt-get install -y bloud`.

All real provisioning (the `bloud` user, subuid/subgid, `/var/lib/bloud`,
linger, the sysctl drop-in, the service) stays in the package's maintainer
scripts, exactly as today. The installer becomes shorter, not longer.

## What retires

- `gh release create` and the `deb-<UTC timestamp>` tag creation in
  `release.yml`.
- The release-resolution and `SHA256SUMS` checksum logic in `install.sh` (the
  checksum now comes from the signed repo, not a same-origin text file).
- The `SHA256SUMS` asset itself.

The historical immutable releases and tags remain as a read-only trail; they
are simply no longer produced or consumed.

## Composition with catalog-update reconciliation

`apt upgrade` and the reconciler are the two halves of one update:

1. **Delivery.** The new package installs a new host-agent binary and a new
   catalog at `/usr/share/bloud/apps`.
2. **Application.** host-agent restarts (or `POST /api/apps/refresh-catalog`)
   and the catalog-update reconciliation plan
   (`docs/plans/catalog-update-reconciliation.md`) makes the running install
   converge to the new catalog: image bumps recreate containers, dropped
   containers are pruned, SSO strategy changes deprovision the old outpost.

Without step 2, an `apt upgrade` would deliver a new catalog and never apply it
to installed apps, because the graph nodes persist as RUNNING across a restart
and cold-start convergence leaves them alone. The two plans are the delivery
half and the application half of the same update.

## Phases

1. **Version scheme.** Replace `packageVersion` with the semver-from-tags
   scheme; unit-test the monotonic cases (tag, N-commits-after, no-tag,
   dirty). No repo yet; the `.deb` filename and control version change.
2. **Repo skeleton + key.** Generate the key, commit the public keyring, add
   the repo-generation script (`apt-ftparchive` + `gpg`) under `packaging/`.
3. **Publish job.** Rework `release.yml` to append to `pool/`, regenerate the
   index, sign, and push the Pages branch. Manual `workflow_dispatch` first.
4. **Bootstrap.** Rewrite `install.sh` to the source+key bootstrap; remove the
   release-resolution path.
5. **End-to-end.** A fresh Debian VM adds the source and installs; a second
   push produces a higher version and `apt upgrade` updates the package in
   place, keeps `/var/lib/bloud`, and the reconciler converges installed apps
   to the new catalog.

## Validation

Three layers: unit, a containerized `apt` consumer, and a VM upgrade round-trip.

### Fast tier (unit)

- `TestPackageVersion_Monotonic`: generate the version for "tag `v1.2.3`",
  "N commits after", "no tag", and "dirty", and assert pairwise ordering with
  `dpkg --compare-versions`. This makes the semver scheme provable rather than
  asserted in prose.
- `TestRepoGenerate_ProducesValidIndex`: generate a repo into a temp dir from a
  built `.deb` and a test key; assert `Packages` lists the `.deb` with a
  matching checksum, `Release` checksums match the files, and `gpg --verify`
  accepts `InRelease`.

### Container (fast)

`packaging/scripts/verify-repo.sh`, run in `debian:trixie` like the existing
`deb-verify.yml`, consumes the generated repo as a client would, without
needing systemd:

1. Serve the repo over a local HTTP server.
2. Install the test keyring and add the source.
3. `apt-get update` succeeds (proves the index and the signature).
4. `apt-cache policy bloud` shows the candidate came from the repo.
5. `apt-get install --download-only bloud` fetches the `.deb`, proving the
   whole `InRelease` → `Packages` → `.deb` chain, without running `postinst`.

The upgrade ordering is provable in the same container: publish v1, observe the
candidate; add v2, regenerate, re-serve, `apt-get update`, and observe the
newer candidate.

### VM (integration)

A clean Debian VM runs the one thing the container cannot: a real install with
systemd. `install.sh` bootstrap, then `apt install bloud`; the service comes up
and the first convergence finishes. Then publish v2 and `apt update && apt
upgrade`: the version bumps, `/var/lib/bloud` survives, the service restarts,
and the reconciler applies the new catalog. That final step is the end-to-end
hand-off to the catalog-update reconciliation plan.

## Acceptance criteria

- `apt update` against the Pages origin succeeds with the key installed, and
  fails (or refuses) without it.
- Two consecutive builds produce versions where the newer sorts higher under
  `dpkg --compare-versions`, and `apt upgrade` installs the newer.
- `apt upgrade` keeps `/var/lib/bloud` and the service comes back up; the
  upgrade path is exercised on a clean Debian VM.
- The pool retains at most K versions per architecture and the index always
  points at the newest.
- `install.sh` provisions nothing except the keyring and the source list.

## Risks and open questions

- **Signing key is a single point of trust and of failure.** A lost private key
  strands updates; a leaked one poisons them. The CI-only subkey mitigates the
  leak, but rotation and revocation need a follow-up.
- **GitHub Pages size and bandwidth.** The pool grows per push; retention (K)
  caps size, and Pages soft limits are ample for a small project, but the cap
  is worth revisiting if the catalog ships many large images.
- **First tag cadence.** Until the first `vX.Y.Z` tag everything is `0.0.0+N`.
  That is monotonic and safe, but the first named release is a one-line manual
  step, not an enforced gate.
- **arm64.** The repo declares `amd64` only today; `arm64` is a later suite or
  architecture addition, not a redesign.
- **`bloud init` preflight** (spec Phase 7) is orthogonal and still absent;
  the apt repo makes the package reachable but does not replace first-run host
  configuration.

## References

- `docs/operations/packaging.md` (current build, install layout, service model)
- `docs/plans/catalog-update-reconciliation.md` (the application half)
- `cli/package.go` (`packageVersion`), `.github/workflows/release.yml`,
  `install.sh`
- `docs/specs/spec.md` Phase 7 (`.deb` packaging and `bloud init`)
