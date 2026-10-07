#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
#
# backup.sh: quiesce Bloud, archive its state, then bring it back up.
#
# Bloud's state is one directory (BLOUD_DATA_DIR, /var/lib/bloud for the
# packaged install): the SQLite database at the root, secrets.json, the
# generated Traefik config, and one private tree per app under apps/. A backup
# that copied those files while Postgres and the apps kept writing would
# capture a database mid-transaction, so this script stops the host-agent and
# every container Bloud manages first, captures the tree, and starts both
# again.
#
# The shared media/ and downloads/ trees are left out by default: they are a
# media library, not app state, and they dwarf everything else. Pass
# --include-shared to archive them too.
#
# Rootless Podman stores its images and overlay layers under
# $HOME/.local/share/containers, and the bloud user's home is the data
# directory. That store is reproducible from the image tags in the catalog, so
# it is excluded; app volumes are bind mounts under apps/ and are not.
#
# The archive holds credentials (secrets.json, every app config). It is
# created mode 0600.
#
# Usage: sudo ./backup.sh [options]
#
#   --data-dir DIR    Bloud data dir (default: $BLOUD_DATA_DIR, else /var/lib/bloud)
#   --output-dir DIR  Where archives go (default: $BLOUD_BACKUP_DIR, else /var/backups/bloud)
#   --include-shared  Also archive media/ and downloads/
#   --keep N          Delete all but the newest N archives (default: keep all)
#   --label NAME      Prefix the archive name with NAME
#   --no-stop         Do not stop Bloud. The archive is crash-consistent, not clean.
#   --keep-stopped    Start the containers again but leave the host-agent down
#   --no-wait         Do not wait for the host-agent API after restarting it
#   -h, --help        Show this help
#
# Environment: BLOUD_DATA_DIR, BLOUD_BACKUP_DIR, BLOUD_USER (bloud),
# BLOUD_UNIT (bloud-host-agent.service), BLOUD_PORT (3000),
# BLOUD_BACKUP_WAIT_SECS (300).

set -eu

PROG=backup.sh

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
include_shared=0
keep=0
do_stop=1
keep_stopped=0
do_wait=1
label=""

while [ $# -gt 0 ]; do
	case "$1" in
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
	--include-shared)
		include_shared=1
		shift
		;;
	--keep)
		[ $# -ge 2 ] || die "--keep needs a value"
		keep="$2"
		shift 2
		;;
	--label)
		[ $# -ge 2 ] || die "--label needs a value"
		label="$2"
		shift 2
		;;
	--no-stop)
		do_stop=0
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
	*)
		die "unknown option: $1 (try --help)"
		;;
	esac
done

case "$keep" in
'' | *[!0-9]*) die "--keep must be a non-negative integer" ;;
esac
case "$label" in
*/*) die "--label must not contain a slash" ;;
esac

[ "$(id -u)" -eq 0 ] || die "run this through sudo"

[ -d "$data_dir" ] || die "data directory not found: $data_dir (is Bloud installed? pass --data-dir)"
data_dir="$(cd "$data_dir" && pwd -P)"
if [ ! -f "$data_dir/bloud.db" ] && [ ! -d "$data_dir/apps" ]; then
	die "$data_dir does not look like a Bloud data directory (no bloud.db or apps/); pass --data-dir"
fi

mkdir -p "$out_dir" || die "cannot create output directory: $out_dir"
out_dir="$(cd "$out_dir" && pwd -P)"
case "$out_dir/" in
"$data_dir"/*) die "output directory $out_dir is inside the data directory $data_dir" ;;
esac

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

stopped_containers=""
stopped_agent=0
resumed=0

checkpoint_sqlite() {
	[ -f "$data_dir/bloud.db" ] || return 0
	if command -v sqlite3 >/dev/null 2>&1; then
		log "checkpointing bloud.db"
		run_as_bloud sqlite3 "$data_dir/bloud.db" 'PRAGMA wal_checkpoint(TRUNCATE);' >/dev/null 2>&1 ||
			warn "could not checkpoint bloud.db; archiving it as-is"
	else
		warn "sqlite3 not found; archiving bloud.db as-is"
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
			die "could not stop $unit; aborting so the archive is not captured mid-write"
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
	timeout="${BLOUD_BACKUP_WAIT_SECS:-300}"
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
# trap so a failed archive does not leave the instance down, and it is
# idempotent because the trap fires on both the failure and the success path.
resume() {
	if [ "$do_stop" -eq 0 ] || [ "$resumed" -eq 1 ]; then
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

prune_archives() {
	[ "$keep" -gt 0 ] || return 0
	old="$(ls -1t "$out_dir"/bloud-*.tar.gz 2>/dev/null | tail -n +"$((keep + 1))" || true)"
	[ -n "$old" ] || return 0
	printf '%s\n' "$old" | while IFS= read -r file; do
		log "pruning old archive: $file"
		rm -f "$file"
	done
}

# EXIT always runs resume, including when a signal exits the shell through
# the INT/TERM traps below, so a Ctrl-C during a long archive never leaves the
# instance down.
trap 'resume' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [ "$do_stop" -eq 1 ]; then
	stop_agent
	stop_containers
	checkpoint_sqlite
else
	warn "--no-stop: not stopping Bloud, the archive may capture files mid-write"
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
archive="$out_dir/bloud-${label:+$label-}$stamp.tar.gz"
if [ -e "$archive" ]; then
	archive="$out_dir/bloud-${label:+$label-}$stamp-$$.tar.gz"
fi

set -- -czf "$archive" -C "$data_dir"
if [ "$include_shared" -eq 0 ]; then
	set -- "$@" --exclude=./media --exclude=./downloads
fi
set -- "$@" \
	--exclude=./.local/share/containers \
	--exclude=./.cache \
	--exclude=./lost+found \
	.

log "archiving $data_dir"
if ! tar "$@"; then
	rm -f "$archive"
	die "tar failed; no archive was written"
fi
chmod 600 "$archive"
log "archive: $archive ($(du -h "$archive" | cut -f1))"

prune_archives
