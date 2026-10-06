#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# update.sh: back up Bloud, then upgrade the installed package.
#
# The update path is the published .deb, installed by install.sh next to this
# script. install.sh resolves the newest release, verifies its checksum, and
# hands it to apt, whose postinst restarts the host-agent. This script adds the
# one thing that upgrade is missing: it takes a clean backup first, so an
# upgrade that goes wrong has a restore point. If install.sh fails, Bloud is
# already back up and running the previous version.
#
# Expects install.sh (and backup.sh) to sit next to this script in a checkout.
# Run it through sudo, like install.sh.
#
# Usage: sudo ./update.sh [options]
#
#   --no-backup       Skip the backup (not recommended)
#   --include-shared  Forward --include-shared to backup.sh
#   --keep N          Forward --keep N to backup.sh
#   --backup-dir DIR  Forward --output-dir DIR to backup.sh
#   -h, --help        Show this help
#
# Environment: BLOUD_PORT (3000), BLOUD_UPDATE_WAIT_SECS (300).

set -eu

PROG=update.sh
script_dir="$(cd "$(dirname "$0")" && pwd -P)"
backup_sh="$script_dir/backup.sh"
install_sh="$script_dir/install.sh"
port="${BLOUD_PORT:-3000}"

log() { printf '==> %s\n' "$*"; }
warn() { printf '%s: %s\n' "$PROG" "$*" >&2; }
die() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}

usage() {
	sed -n '3,24p' "$0" | sed 's/^# \{0,1\}//'
}

do_backup=1
backup_include=0
backup_keep=""
backup_dir=""

while [ $# -gt 0 ]; do
	case "$1" in
	--no-backup)
		do_backup=0
		shift
		;;
	--include-shared)
		backup_include=1
		shift
		;;
	--keep)
		[ $# -ge 2 ] || die "--keep needs a value"
		backup_keep="$2"
		shift 2
		;;
	--backup-dir | --output-dir)
		[ $# -ge 2 ] || die "$1 needs a value"
		backup_dir="$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1 (try --help)"
		;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run this through sudo"
[ -f "$install_sh" ] || die "install.sh not found next to this script ($install_sh)"

if [ "$do_backup" -eq 1 ]; then
	[ -f "$backup_sh" ] || die "backup.sh not found next to this script ($backup_sh)"
	# Rebuild the argument list backup.sh receives from the options above.
	set --
	if [ "$backup_include" -eq 1 ]; then
		set -- "$@" --include-shared
	fi
	if [ -n "$backup_keep" ]; then
		set -- "$@" --keep "$backup_keep"
	fi
	if [ -n "$backup_dir" ]; then
		set -- "$@" --output-dir "$backup_dir"
	fi
	log "backing up before the update"
	"$backup_sh" "$@"
else
	warn "--no-backup: upgrading with no restore point"
fi

log "upgrading Bloud (install.sh)"
"$install_sh"

if command -v dpkg-query >/dev/null 2>&1; then
	version="$(dpkg-query -W -f='${Version}' bloud 2>/dev/null || true)"
	if [ -n "$version" ]; then
		log "installed version: $version"
	fi
fi

if command -v curl >/dev/null 2>&1; then
	timeout="${BLOUD_UPDATE_WAIT_SECS:-300}"
	elapsed=0
	while [ "$elapsed" -lt "$timeout" ]; do
		if curl -fsS -o /dev/null --max-time 5 "http://127.0.0.1:$port/api/health" 2>/dev/null; then
			log "Bloud is healthy on http://127.0.0.1:$port"
			exit 0
		fi
		sleep 5
		elapsed=$((elapsed + 5))
	done
	warn "Bloud did not answer on http://127.0.0.1:$port within ${timeout}s"
	warn "check: systemctl --user status bloud-host-agent.service"
	exit 1
fi

log "update complete"
