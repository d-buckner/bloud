#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Verifies a built Bloud .deb's dependency model against a real Debian package
# index. Run it inside a Debian container; CI uses debian:trixie, the
# supported release:
#
#   ./bloud package --arch amd64
#   docker run --rm \
#     -v "$PWD/dist:/deb:ro" \
#     -v "$PWD/packaging/scripts:/scripts:ro" \
#     debian:trixie \
#     /bin/sh /scripts/verify-deps.sh "/deb/$(ls dist | grep '\.deb$')"
#
# Four checks that a rendered control file cannot make on its own:
#
#   1. every declared dependency exists in the release archive, so a typo or a
#      package that is not in Debian fails here instead of on a user's box
#   2. the podman version floor holds against the release candidate
#   3. apt resolves the whole set and its install plan pulls podman
#   4. dpkg reads the relationships, so `dpkg -i` on a system without them
#      reports podman and the rest as unmet
set -eu

DEB="${1:?usage: verify-deps.sh /path/to/bloud_<version>_<arch>.deb}"
if [ ! -f "$DEB" ]; then
	printf 'FAIL: no such package: %s\n' "$DEB" >&2
	exit 1
fi

fail() {
	printf 'FAIL: %s\n' "$1" >&2
	exit 1
}
ok() { printf '  ok: %s\n' "$1"; }

# Names only: alternatives split on "|", version constraints dropped.
depends_names() {
	printf '%s\n' "$DEPENDS" |
		tr ',|' '\n\n' |
		sed 's/(.*//' |
		sed 's/^ *//; s/ *$//' |
		grep -v '^$' |
		sort -u
}

is_declared() { grep -qx "$1" /tmp/bloud-declared; }

# A dependency counts when the install plan pulls it, or when this image
# already has it (then there is nothing to pull and the plan stays quiet).
is_planned_or_installed() {
	if grep -q "^Inst $1 " /tmp/bloud-apt-plan.out 2>/dev/null; then
		return 0
	fi
	dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'
}

apt-get update -qq >/dev/null 2>&1 || fail "apt-get update failed; no package index to verify against"

DEPENDS="$(dpkg-deb -f "$DEB" depends)"
[ -n "$DEPENDS" ] || fail "$(basename "$DEB") declares no Depends"

printf 'Package: %s\nDepends: %s\n\n' "$(basename "$DEB")" "$DEPENDS"

printf '== 1. every declared dependency exists in this Debian release ==\n'
depends_names >"/tmp/bloud-declared"
while read -r name; do
	candidate="$(apt-cache policy "$name" | sed -n 's/^  Candidate: //p')"
	if [ -z "$candidate" ] || [ "$candidate" = "(none)" ]; then
		fail "$name is not available in this Debian release"
	fi
	ok "$name -> $candidate"
done <"/tmp/bloud-declared"

printf '\n== 2. the runtime set host-agent needs is declared ==\n'
for name in podman uidmap dbus-user-session ca-certificates adduser systemd; do
	if ! is_declared "$name"; then
		fail "Depends does not declare $name"
	fi
	ok "$name declared"
done
if is_declared passt || is_declared slirp4netns; then
	ok "a rootless network provider is declared"
else
	fail "no rootless network provider declared (passt or slirp4netns)"
fi

printf '\n== 3. the podman floor holds against the release candidate ==\n'
floor="$(printf '%s\n' "$DEPENDS" |
	tr ',' '\n' |
	sed -n 's/^ *podman *(\([^)]*\))$/\1/p' |
	sed 's/^>= *//')"
[ -n "$floor" ] || fail "podman is declared with no version floor"
podman_candidate="$(apt-cache policy podman | sed -n 's/^  Candidate: //p')"
if ! dpkg --compare-versions "$podman_candidate" ge "$floor"; then
	fail "podman candidate $podman_candidate does not satisfy >= $floor"
fi
ok "podman $podman_candidate satisfies >= $floor (the libpod API host-agent calls)"

printf '\n== 4. apt resolves the set and pulls podman ==\n'
if ! apt-get install -s -y "$DEB" >/tmp/bloud-apt-plan.out 2>&1; then
	sed -n '1,40p' /tmp/bloud-apt-plan.out
	fail "apt could not resolve the package's dependencies"
fi
for name in podman uidmap dbus-user-session ca-certificates adduser systemd; do
	if ! is_planned_or_installed "$name"; then
		fail "the apt install plan does not include $name"
	fi
	ok "$name in the install plan"
done
if ! is_planned_or_installed passt && ! is_planned_or_installed slirp4netns; then
	fail "the apt install plan has no rootless network provider"
fi
ok "the install plan has a rootless network provider"

printf '\n== 5. dpkg reads the relationships ==\n'
if dpkg -i "$DEB" >/tmp/bloud-dpkg.out 2>&1; then
	ok "dpkg -i accepted the package (this image already satisfied the set)"
else
	if grep -q 'dependency problems' /tmp/bloud-dpkg.out && grep -q 'podman' /tmp/bloud-dpkg.out; then
		ok "dpkg -i reports the unmet dependencies, podman among them"
	else
		sed -n '1,40p' /tmp/bloud-dpkg.out
		fail "dpkg -i failed for a reason other than unmet dependencies"
	fi
fi

printf '\nPASS: %s declares the runtime dependency model\n' "$(basename "$DEB")"
