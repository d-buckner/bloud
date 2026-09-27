#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Bloud installer: fetch the published .deb and install it with apt.
#
# Nothing else. The provisioning a fresh install needs (the unprivileged
# "bloud" user, its subuid/subgid ranges, /var/lib/bloud, linger, the
# unprivileged-port sysctl, the user-level host-agent service) belongs to the
# package's own maintainer scripts, not to this file: this script is not
# covered by the packaging tests, so anything that lived here could drift from
# the package it installs.
#
#   curl -fsSL https://raw.githubusercontent.com/d-buckner/bloud/main/install.sh | sudo sh

set -eu

REPO="d-buckner/bloud"

# The repository publishes immutable releases: GitHub's release immutability
# setting is on, so once a release is published its assets can never be added,
# replaced, or deleted, and its tag can never be moved. A rolling fixed-name
# asset is therefore impossible, because the release behind it would have to be
# rewritten on every push. Each push to main publishes its own immutable
# deb-<UTC timestamp> prerelease instead, and the newest of those is resolved
# here, at install time.
#
# Unauthenticated API calls are capped at 60 per hour per IP. Export
# GITHUB_TOKEN (or GH_TOKEN) before running this to raise that ceiling when
# installing from a shared address.
api="https://api.github.com/repos/$REPO/releases?per_page=30"
token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
if [ -n "$token" ]; then
	json="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
		-H "Authorization: Bearer $token" "$api")"
else
	json="$(curl -fsSL -H 'Accept: application/vnd.github+json' "$api")"
fi

# The release list is newest first, so the first .deb asset is the current
# build. Matching the .deb suffix rather than the tag prefix keeps this working
# if the repository ever ships another kind of release alongside it.
url="$(printf '%s' "$json" |
	tr ',' '\n' |
	sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\.deb\)".*/\1/p' |
	head -n 1)"

if [ -z "$url" ]; then
	printf 'bloud: no published .deb found in %s\n' "$REPO" >&2
	printf '       Browse https://github.com/%s/releases by hand.\n' "$REPO" >&2
	printf '       A rate-limited API call fails this way too: 60 unauthenticated\n' >&2
	printf '       requests per hour per IP. Set GITHUB_TOKEN and retry.\n' >&2
	exit 1
fi

# Test hook, used by .github/workflows/release.yml to assert that this resolver
# picks the release it just published. Prints the URL and nothing else, and runs
# unprivileged.
if [ "${BLOUD_INSTALL_URL_ONLY:-}" = "1" ]; then
	printf '%s\n' "$url"
	exit 0
fi

[ "$(id -u)" -eq 0 ] || { echo "bloud: run this through sudo" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

printf '==> Downloading %s\n' "$url"
curl -fsSL -o "$tmp/bloud.deb" "$url"

printf '==> Installing (apt pulls podman, uidmap, and the rest)\n'
apt-get install -y "$tmp/bloud.deb"

printf '==> Done. The first run converges Traefik and Authentik before the\n'
printf '    dashboard answers: give it a minute or two, then open http://%s\n' \
	"$(hostname -I 2>/dev/null | cut -d' ' -f1 || echo localhost)"
printf '    and set your host under Settings -> Hosts.\n'
