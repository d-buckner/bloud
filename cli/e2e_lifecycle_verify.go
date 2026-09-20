// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"encoding/json"
)

// runInstallFlow installs Jellyfin through the host-local API in --host-only
// mode, or through the browser (ensure user + Playwright install/login flow)
// otherwise.
func (r *lifecycle) runInstallFlow() error {
	if r.cfg.hostOnly {
		r.step("Installing Jellyfin through the host-local API")
		return r.remoteRun(remoteInstallJellyfinScript, r.cfg.remoteDir)
	}
	r.step("Ensuring the E2E user exists")
	payload, err := json.Marshal(map[string]string{"username": r.cfg.username, "password": r.cfg.password})
	if err != nil {
		return err
	}
	if err := r.remoteRun(remoteEnsureUserScript, string(payload)); err != nil {
		return err
	}
	r.step("Running Jellyfin browser install and login flow")
	return runPlaywright(r.cfg.root, r.cfg.username, r.cfg.password, r.apiTokenBestEffort())
}

// verifyAfterRestart restarts Jellyfin and the host-agent and re-runs the
// browser flow (unless --host-only) to prove the lifecycle survives restarts.

var remoteResetJellyfinScript = `tok="$(cat ${1}/data/host-agent-api-token)"
installed="$(curl -sS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed || printf '[]')"
if printf '%s' "$installed" | grep -q '"name":"jellyfin"'; then
  curl -sS -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall >/dev/null || true
  deadline=$((SECONDS + 120))
  until ! curl -sS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed | grep -q '"name":"jellyfin"'; do
    if ((SECONDS >= deadline)); then exit 1; fi
    sleep 2
  done
fi
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
installed="$(curl -sS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed || printf '[]')"
! printf '%s' "$installed" | grep -q '"name":"jellyfin"'`

var remoteInstallJellyfinScript = `tok="$(cat ${1}/data/host-agent-api-token)"
http_code="$(curl -sS -o /dev/null -w '%%{http_code}' -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{}' http://localhost:3000/api/apps/jellyfin/install)"
printf 'install response: %%s\n' "$http_code"
test "$http_code" -ge 200 && test "$http_code" -lt 300
deadline=$((SECONDS + 300))
until curl -sS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed | grep -q '"status":"running".*"name":"jellyfin"\|"name":"jellyfin".*"status":"running"'; do
  if ((SECONDS >= deadline)); then echo "timed out waiting for jellyfin to reach running"; exit 1; fi
  sleep 3
done
printf 'jellyfin is running\n'`

var remoteAssertInstalledScript = `
set -e
tok="$(cat ${2}/data/host-agent-api-token)"
managed=$(podman inspect -f '{{ index .Config.Labels "io.bloud.managed" }}' apps-jellyfin)
test "$managed" = true || { echo "FAIL: io.bloud.managed=$managed" >&2; exit 1; }
app=$(podman inspect -f '{{ index .Config.Labels "io.bloud.app" }}' apps-jellyfin)
test "$app" = jellyfin || { echo "FAIL: io.bloud.app=$app" >&2; exit 1; }
running=$(podman inspect -f '{{ .State.Running }}' apps-jellyfin)
test "$running" = true || { echo "FAIL: container running=$running" >&2; exit 1; }
# Retry the Jellyfin health check: the API oscillates between 200 and 503
# "Server is loading" during first-run init, even after PostStart completes.
deadline=$((SECONDS + 60))
until curl -fsS http://localhost:8096/health >/dev/null; do
  if ((SECONDS >= deadline)); then
    echo "FAIL: Jellyfin health check timed out after 60s" >&2
    curl -sS http://localhost:8096/health >&2 || true
    exit 1
  fi
  sleep 2
done
curl -fsS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed | grep -q '"catalog_id":"jellyfin"' || { echo "FAIL: jellyfin not in installed apps" >&2; exit 1; }
grep -q 'jellyfin' "$1/apps-routes.yml" || { echo "FAIL: jellyfin route not in $1/apps-routes.yml" >&2; exit 1; }
echo "OK: installed Jellyfin host state verified"
`
