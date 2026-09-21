#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (c) 2026 Daniel Buckner
#
# Debian prerm: stop the host-agent before the package is removed. An upgrade
# leaves the running service alone; postinst restarts it with the new binary.
set -e

BLOUD_UNIT=bloud-host-agent.service

case "$1" in
remove | purge)
	if [ -d /run/systemd/system ] && getent passwd bloud >/dev/null; then
		uid=$(id -u bloud)
		runuser -u bloud -- env XDG_RUNTIME_DIR="/run/user/$uid" \
			systemctl --user disable --now "$BLOUD_UNIT" >/dev/null 2>&1 || true
	fi
	;;
esac

exit 0
