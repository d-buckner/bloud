# Changelog

The changes Bloud publishes, newest first. Every release gets one section here,
and the release workflow uses that section as the release notes. A release with
no section cannot be published: `./bloud changelog --check` fails the build, and
the release workflow refuses to publish empty notes.

Each entry names one user-visible change and the pull request that brought it.
The text comes from the `## Changelog` section of that pull request, not from the
commit message. [docs/operations/releases.md](docs/operations/releases.md)
records the process end to end.

The groups are the Keep a Changelog set: Added, Changed, Deprecated, Removed,
Fixed, and Security. The file format is strict, because a machine reads it to
build release notes. `./bloud changelog --check` reports any line it cannot
parse, with its line number.

The v0.0.0 section is a baseline. A person wrote it by hand, before this process
started, to record what the alpha does. Every section below it comes from the
pull requests that produced the release.

## [v0.0.0] - 2026-10-17

### Added

- Add an app from the dashboard, and Bloud sets up its reverse proxy route,
  single sign-on, and database for it.
- Apps declare what they provide and what they consume in `metadata.yaml`, and
  Bloud wires the two together: API keys, OIDC clients, and LDAP accounts reach
  the apps that need them.
- One login covers the catalog through Authentik. An app joins with one of four
  strategies: `native-oidc`, `ldap`, `forward-auth`, or `none`.
- The instance ships as a Debian package. `./bloud package` builds it, and
  `install.sh` installs the newest published one and checks it against the
  published checksum.
- The orchestrator reconciles every installed app on a timer, so the instance
  makes itself match what you asked for after a crash or a reboot.
