# Releases

A release is a semver tag, and its notes are the matching section of
[`../../CHANGELOG.md`](../../CHANGELOG.md). Nothing publishes a release with
placeholder notes: a tag with no section fails before anything is uploaded.

Two commands carry the process:

| Command | What it does |
|---|---|
| `./bloud changelog --check` | Reports a changelog the release workflow cannot publish from. A command in the fast validation tier. |
| `./bloud changelog collect --bump <major\|minor\|patch>` | Gathers the entries of the pull requests merged since the last tag, and writes the next section into `CHANGELOG.md`. |
| `./bloud changelog --notes <v> --out FILE` | Renders the release notes for one version. The release workflow calls this. |
| `./bloud changelog --check-pr FILE` | Checks one pull request body for its Changelog section. CI calls this with the body from the event payload. |

## Every pull request carries its entry

The pull request template has a `## Changelog` section. It takes one bullet per
user-visible change, and each bullet starts with a category:

```markdown
## Changelog

- Added: A searchable time zone picker under Settings.
- Fixed: The waiting page no longer hides an app's own 503.
```

The categories are Added, Changed, Deprecated, Removed, Fixed, and Security. The
text after the colon is a sentence: it appears in the release notes under that
category, next to the sentences from every other pull request. Write `n/a` on
its own line when nothing a user sees changes.

`.github/workflows/pr-changelog.yml` reads the section from the pull request body
in the workflow event payload, so the check needs no API call. It is the same code
as `./bloud changelog --check-pr`, so you can run it on a body you saved to a
file.

**The pull request body is the only copy of the entry.** This repository
squash-merges, and the commit body that lands on `main` is empty. An entry that
is not in the pull request body is recorded nowhere, and the release notes then
omit the change. That is why the check is a build failure rather than advice.

## Cutting a release

Run this on `main`, with the pull requests you want in the release already merged:

```bash
git checkout main && git pull
./bloud changelog collect --bump minor
git add CHANGELOG.md
git commit -m "release: v0.2.0"
git tag v0.2.0
git push origin main --follow-tags
```

`collect` reads the tags, lists the pull requests merged since the newest one,
fetches each body through `gh`, and writes the new section at the top of
`CHANGELOG.md`. It prints the section it wrote. It needs a working `gh` login,
because the bodies live in the forge and not in git.

`collect` fails, and changes nothing, when:

- a merged pull request has no usable Changelog section. It names them, and you
  add the section to each one and run again.
- no pull request since the last tag carries an entry. A release with nothing to
  say is a release that should not exist yet.
- the version already has a section, or is not newer than the newest one.

Pick the bump by what changed:

| Bump | Use it when |
|---|---|
| `patch` | Fixes only. No new capability, and no change an operator must act on. |
| `minor` | A new capability: an app, a setting, a contract, a command. |
| `major` | A change an operator must act on to upgrade: a data migration with no rollback, or a removed interface. |

## What publishes, and what does not

| Event | Result |
|---|---|
| Push to `main` | Builds the `.deb` and keeps it as a workflow artifact. Publishes nothing. |
| Push a `v*` tag | Builds the `.deb` at that version, renders the notes from the changelog section, and publishes a GitHub release with the `.deb` and `SHA256SUMS`. |
| `workflow_dispatch` | Builds the `.deb` and keeps it as an artifact. Publishes nothing. |

The tag and its notes are the same change: the section is committed before the
tag is cut, so the tag points at a commit that already carries the text the
release page shows.

Every release this workflow publishes is immutable, because the repository has
GitHub's release immutability setting enabled. See
[packaging.md](packaging.md#why-there-is-no-rolling-asset) for what that rules
out, and for how [`install.sh`](../../install.sh) resolves the newest release at
install time.

## The checks, and where each one runs

| Check | Where | What it catches |
|---|---|---|
| `./bloud changelog --check-pr -` | `.github/workflows/pr-changelog.yml`, on every pull request | A pull request with no Changelog section, a section left as the template left it, a bullet with no category. |
| `./bloud changelog --check` | The fast validation tier, so locally, in CI, and on Forgejo | A section with no entries, releases out of order, a line the parser cannot read, and a `v*` tag with no section. |
| `./bloud changelog --notes` | `.github/workflows/release.yml`, before `gh release create` | A tag whose section is missing or renders nothing. The workflow exits before it publishes. |

`--check` is the one that closes the old gap. Before this process,
`.github/workflows/release.yml` wrote this note on every release:

```text
Automated build of the Bloud .deb from f1a025d9c3828d9089cb3da295e52ddffed52a2f.
```

That string said nothing about what the release contained, and it was the same
for every release. Now the notes are the changelog section, and a release with
no section does not get published.

## The file format

`CHANGELOG.md` is strict, because a machine reads it to build release notes and
silently ignores what it cannot parse.

```markdown
## [v0.2.0] - 2026-02-10

### Added

- A searchable time zone picker under Settings. (#365)

### Fixed

- The waiting page no longer hides an app's own 503. (#311)
```

- A release heading is `## [vMAJOR.MINOR.PATCH] - YYYY-MM-DD`.
- A group heading is one of the six categories, and a group appears once, in the
  canonical order.
- An entry is a bullet. An indented line continues the entry above it, so a long
  entry can wrap at the column count the rest of the tree uses.
- Releases go newest first.
- Nothing else may appear inside a release section. The header above the first
  release is free text.

`--check` reports the line number of the first thing it cannot read.
