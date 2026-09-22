#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# Debian postrm: on purge, drop the runtime user and its data directory. An
# upgrade or plain removal keeps /var/lib/bloud so app data survives.
set -e

case "$1" in
purge)
	rm -rf /var/lib/bloud
	if getent passwd bloud >/dev/null; then
		loginctl disable-linger bloud >/dev/null 2>&1 || true
		deluser bloud >/dev/null 2>&1 || userdel bloud >/dev/null 2>&1 || true
	fi
	;;
esac

exit 0
