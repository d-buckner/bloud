#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Debian postinst: provision the dedicated unprivileged user, its rootless
# Podman ranges, the data directory, and the user-level host-agent service.
set -e

BLOUD_USER=bloud
BLOUD_HOME=/var/lib/bloud
BLOUD_UNIT=bloud-host-agent.service

# The runtime is deliberately rootless (see docs/specs/spec.md, "Supported
# Environment"): a dedicated system user owns the Podman socket and the store.
if ! getent passwd "$BLOUD_USER" >/dev/null; then
	adduser --system --group --home "$BLOUD_HOME" --no-create-home \
		--shell /usr/sbin/nologin "$BLOUD_USER"
fi

# Rootless Podman maps the user into a user namespace, which needs subordinate
# UID/GID ranges.
if ! grep -q "^$BLOUD_USER:" /etc/subuid 2>/dev/null; then
	usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$BLOUD_USER"
fi

# Runtime data: SQLite database, secrets.json, and the Traefik dynamic config.
install -d -o "$BLOUD_USER" -g "$BLOUD_USER" -m 0750 "$BLOUD_HOME"

if [ -d /run/systemd/system ]; then
	# Apply the unprivileged-port sysctl now; the drop-in also applies at boot.
	systemctl restart systemd-sysctl.service >/dev/null 2>&1 || true

	# Keep the user's systemd instance, and the Podman socket it owns, alive
	# across logout and reboot.
	loginctl enable-linger "$BLOUD_USER" >/dev/null 2>&1 || true

	uid=$(id -u "$BLOUD_USER")
	run_user() {
		runuser -u "$BLOUD_USER" -- env XDG_RUNTIME_DIR="/run/user/$uid" systemctl --user "$@"
	}

	# The user manager starts on demand via linger; retry until it answers.
	i=0
	while [ "$i" -lt 15 ]; do
		run_user daemon-reload >/dev/null 2>&1 && break
		i=$((i + 1))
		sleep 1
	done

	run_user enable "$BLOUD_UNIT" >/dev/null 2>&1 || true
	run_user restart "$BLOUD_UNIT" >/dev/null 2>&1 || true
fi

exit 0
