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

# The rolling asset published by .github/workflows/release.yml on every push to
# main. Fixed name, fixed tag, so this URL never has to be edited.
URL="https://github.com/d-buckner/bloud/releases/download/latest/bloud_latest_amd64.deb"

[ "$(id -u)" -eq 0 ] || { echo "bloud: run this through sudo" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "==> Downloading $URL"
curl -fsSL -o "$tmp/bloud.deb" "$URL"

echo "==> Installing (apt pulls podman, uidmap, and the rest)"
apt-get install -y "$tmp/bloud.deb"

echo "==> Done. The first run converges Traefik and Authentik before the"
echo "    dashboard answers: give it a minute or two, then open http://$(hostname -I 2>/dev/null | cut -d' ' -f1 || echo localhost)"
echo "    and set your host under Settings -> Hosts."
