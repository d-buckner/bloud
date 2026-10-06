#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# restore.sh: put a backup.sh archive back, and bring Bloud up on it.
#
# The archive is the whole data directory minus the shared media/ and
# downloads/ trees and the rootless Podman image store, so a restore replaces
# all of Bloud's state (the SQLite database, secrets.json, the Traefik config,
# every app tree) and leaves those three alone. The steps mirror backup.sh:
# stop the host-agent and every container Bloud manages, swap the state in,
# start both again.
#
# The archive is extracted to a staging directory next to the data directory
# first, and its contents are only moved into place once the extraction has
# succeeded and the confirmation has been given. A truncated or foreign
# archive therefore fails without touching the running instance or stopping
# it, and the old state is not deleted until the replacement is already on
# disk.
#
# Restore replaces every path the archive carries. A default backup.sh
# archive has no media/ or downloads/ (and no Podman image store under
# .local/), so the library and the image store survive and an app comes back
# on its existing content; an --include-shared archive restores the trees it
# carries. .local/, .cache/ and lost+found/ are never touched.
#
# Usage: sudo ./restore.sh ARCHIVE [options]
#        sudo ./restore.sh --latest [options]
#
#   --latest          Restore the newest bloud-*.tar.gz in the output directory
#   --data-dir DIR    Bloud data dir (default: $BLOUD_DATA_DIR, else /var/lib/bloud)
#   --output-dir DIR  Where --latest looks (default: $BLOUD_BACKUP_DIR, else /var/backups/bloud)
#   --dry-run         Show what the archive would replace, then exit
#   -y, --yes         Skip the confirmation prompt
#   --keep-stopped    Start the containers again but leave the host-agent down
#   --no-wait         Do not wait for the host-agent API after restarting it
#   -h, --help        Show this help
#
# Environment: BLOUD_DATA_DIR, BLOUD_BACKUP_DIR, BLOUD_USER (bloud),
# BLOUD_UNIT (bloud-host-agent.service), BLOUD_PORT (3000),
# BLOUD_RESTORE_WAIT_SECS (300).

set -eu

PROG=restore.sh

log() { printf '==> %s\n' "$*"; }
warn() { printf '%s: %s\n' "$PROG" "$*" >&2; }
die() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}

usage() {
	sed -n '3,40p' "$0" | sed 's/^# \{0,1\}//'
}

data_dir="${BLOUD_DATA_DIR:-/var/lib/bloud}"
out_dir="${BLOUD_BACKUP_DIR:-/var/backups/bloud}"
bloud_user="${BLOUD_USER:-bloud}"
unit="${BLOUD_UNIT:-bloud-host-agent.service}"
port="${BLOUD_PORT:-3000}"
archive=""
use_latest=0
assume_yes=0
dry_run=0
keep_stopped=0
do_wait=1

while [ $# -gt 0 ]; do
	case "$1" in
	--latest)
		use_latest=1
		shift
		;;
	--data-dir)
		[ $# -ge 2 ] || die "--data-dir needs a value"
		data_dir="$2"
		shift 2
		;;
	--output-dir | --backup-dir)
		[ $# -ge 2 ] || die "$1 needs a value"
		out_dir="$2"
		shift 2
		;;
	--dry-run)
		dry_run=1
		shift
		;;
	-y | --yes)
		assume_yes=1
		shift
		;;
	--keep-stopped)
		keep_stopped=1
		shift
		;;
	--no-wait)
		do_wait=0
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*)
		die "unknown option: $1 (try --help)"
		;;
	*)
		[ -z "$archive" ] || die "unexpected argument: $1 (try --help)"
		archive="$1"
		shift
		;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "run this through sudo"

if [ "$use_latest" -eq 1 ] && [ -z "$archive" ]; then
	[ -d "$out_dir" ] || die "output directory not found: $out_dir"
	archive="$(ls -1t "$out_dir"/bloud-*.tar.gz 2>/dev/null | head -n 1 || true)"
	[ -n "$archive" ] || die "no bloud-*.tar.gz found in $out_dir"
fi
[ -n "$archive" ] || die "no archive given (pass a path, or --latest)"
[ -f "$archive" ] || die "archive not found: $archive"
archive="$(cd "$(dirname "$archive")" && pwd -P)/$(basename "$archive")"

[ -d "$data_dir" ] || die "data directory not found: $data_dir (is Bloud installed? pass --data-dir)"
data_dir="$(cd "$data_dir" && pwd -P)"

# The dot-directories that hold the rootless Podman home, plus the
# filesystem's own lost+found, are never touched. The archive carries a
# .local/share/ skeleton because backup.sh excludes only
# .local/share/containers, so replacing .local wholesale would delete the
# image store. media/ and downloads/ are absent from a default archive and
# are therefore left alone unless the archive was made with --include-shared.
is_protected() {
	case " .local .cache lost+found " in
	*" $1 "*) return 0 ;;
	esac
	return 1
}

# top_names prints the first path component of every entry in the archive.
top_names() {
	tar -tzf "$archive" | sed -e 's|^\./||' -e 's|/.*$||' | grep -v -e '^$' -e '^\.$' | sort -u
}

if [ "$dry_run" -eq 1 ]; then
	log "archive:        $archive"
	log "data directory: $data_dir"
	printf '  %-8s %s\n' "action" "path"
	top_names | while IFS= read -r name; do
		if is_protected "$name"; then
			continue
		fi
		if [ -e "$data_dir/$name" ]; then
			printf '  %-8s %s\n' "replace" "$name"
		else
			printf '  %-8s %s\n' "add" "$name"
		fi
	done
	exit 0
fi

if id -u "$bloud_user" >/dev/null 2>&1; then
	bloud_uid="$(id -u "$bloud_user")"
else
	bloud_uid=""
	warn "user '$bloud_user' not found; running container and service commands as the current user"
fi

# run_as_bloud runs a command as the user that owns the Podman socket and the
# data directory. systemctl and podman both need XDG_RUNTIME_DIR to find that
# user's socket.
run_as_bloud() {
	if [ -n "$bloud_uid" ] && command -v runuser >/dev/null 2>&1; then
		runuser -u "$bloud_user" -- env XDG_RUNTIME_DIR="/run/user/$bloud_uid" "$@"
	else
		"$@"
	fi
}

# Staging sits next to the data directory so moving the restored tree into
# place is a rename, not a copy.
data_parent="$(cd "$(dirname "$data_dir")" && pwd -P)"
staging="$data_parent/.bloud-restore-$$"
stopped_containers=""
stopped_agent=0
resumed=0

cleanup_staging() {
	if [ -n "$staging" ] && [ -d "$staging" ]; then
		rm -rf "$staging"
	fi
}

stop_agent() {
	if ! command -v systemctl >/dev/null 2>&1; then
		warn "systemctl not found; cannot stop $unit"
		return 0
	fi
	if run_as_bloud systemctl --user is-active --quiet "$unit" 2>/dev/null; then
		log "stopping $unit"
		if run_as_bloud systemctl --user stop "$unit"; then
			stopped_agent=1
		else
			die "could not stop $unit; aborting before overwriting state"
		fi
	else
		warn "$unit is not running"
	fi
}

stop_containers() {
	if ! command -v podman >/dev/null 2>&1; then
		warn "podman not found; skipping container stop"
		return 0
	fi
	ids="$(run_as_bloud podman ps -q --filter 'label=io.bloud.managed=true' 2>/dev/null || true)"
	if [ -z "$ids" ]; then
		warn "no running Bloud containers found"
		return 0
	fi
	count="$(printf '%s\n' "$ids" | wc -l | tr -d ' ')"
	log "stopping $count Bloud container(s)"
	# shellcheck disable=SC2086
	run_as_bloud podman stop --time 30 $ids >/dev/null || warn "some containers did not stop cleanly"
	stopped_containers="$ids"
}

start_containers() {
	[ -n "$stopped_containers" ] || return 0
	log "starting the containers back up"
	# shellcheck disable=SC2086
	run_as_bloud podman start $stopped_containers >/dev/null 2>&1 ||
		warn "some containers did not start; check podman ps and the host-agent log"
	stopped_containers=""
}

start_agent() {
	[ "$stopped_agent" -eq 1 ] || return 0
	log "starting $unit"
	if run_as_bloud systemctl --user start "$unit"; then
		stopped_agent=0
	else
		warn "failed to start $unit"
	fi
}

wait_for_api() {
	if ! command -v curl >/dev/null 2>&1; then
		warn "curl not found; not waiting for the host-agent API"
		return 0
	fi
	timeout="${BLOUD_RESTORE_WAIT_SECS:-300}"
	elapsed=0
	while [ "$elapsed" -lt "$timeout" ]; do
		if curl -fsS -o /dev/null --max-time 5 "http://127.0.0.1:$port/api/health" 2>/dev/null; then
			log "host-agent is healthy on http://127.0.0.1:$port"
			return 0
		fi
		sleep 5
		elapsed=$((elapsed + 5))
	done
	warn "host-agent did not answer on http://127.0.0.1:$port within ${timeout}s"
	warn "check: systemctl --user status $unit (as $bloud_user)"
}

# resume undoes the stop, whatever happened in between. It runs from an EXIT
# trap so a failed restore does not leave the instance down, and it is
# idempotent because the trap fires on both the failure and the success path.
resume() {
	if [ "$resumed" -eq 1 ]; then
		return 0
	fi
	resumed=1
	start_containers
	if [ "$stopped_agent" -eq 1 ] && [ "$keep_stopped" -eq 0 ]; then
		start_agent
		if [ "$do_wait" -eq 1 ]; then
			wait_for_api
		fi
	fi
}

cleanup() {
	cleanup_staging
	resume
}

# EXIT always runs cleanup, including when a signal exits the shell through
# the INT/TERM traps below, so an interrupted restore still restarts what it
# stopped.
trap 'cleanup' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Extract before stopping anything: a bad archive fails here, with the
# instance still running and its state untouched.
log "extracting $archive"
mkdir -p "$staging" || die "cannot create staging directory: $staging"
if ! tar -xzf "$archive" -C "$staging"; then
	die "could not extract the archive; nothing was changed"
fi

if [ ! -e "$staging/bloud.db" ] && [ ! -d "$staging/apps" ]; then
	die "archive does not look like a Bloud backup (no bloud.db or apps/); nothing was changed"
fi

if [ "$assume_yes" -eq 0 ]; then
	printf '\n'
	log "this replaces all Bloud state in $data_dir with:"
	log "  $archive"
	printf 'The shared media/ and downloads/ trees are left alone.\n'
	printf "Type 'yes' to continue: "
	read -r answer || answer=""
	if [ "$answer" != "yes" ]; then
		die "aborted; nothing was changed"
	fi
fi

stop_agent
stop_containers

# A database restored under a stale write-ahead log is a corrupt database, so
# the sidecar files go even when the archive does not carry them.
rm -f "$data_dir/bloud.db-wal" "$data_dir/bloud.db-shm" "$data_dir/bloud.db-journal"

for path in "$staging"/* "$staging"/.[!.]* "$staging"/..?*; do
	[ -e "$path" ] || [ -L "$path" ] || continue
	name=${path##*/}
	if is_protected "$name"; then
		continue
	fi
	log "restoring $name"
	rm -rf "$data_dir/$name"
	mv "$path" "$data_dir/$name"
done

log "restore complete"
