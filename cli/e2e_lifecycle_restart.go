// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import ()

// verifyAfterRestart restarts Jellyfin and the host-agent and re-runs the
// browser flow (unless --host-only) to prove the lifecycle survives restarts.
func (r *lifecycle) verifyAfterRestart() error {
	r.step("Restarting Jellyfin and host-agent")
	if err := r.remoteRun(remoteRestartScript, lifecycleHostAgentUnit, r.cfg.remoteDir); err != nil {
		return err
	}
	if !r.cfg.hostOnly {
		r.step("Verifying browser flow after service restarts")
		return runPlaywright(r.cfg.root, r.cfg.username, r.cfg.password, r.apiTokenBestEffort())
	}
	return nil
}

var remoteRestartScript = `tok="$(cat ${2}/data/host-agent-api-token)"
podman restart apps-jellyfin
deadline=$((SECONDS + 300))
until curl -fsS http://localhost:8096/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done
systemctl --user restart "$1"
deadline=$((SECONDS + 300))
until curl -fsS http://localhost:3000/api/health >/dev/null; do
  if ((SECONDS >= deadline)); then exit 1; fi
  sleep 2
done
test "$(podman inspect -f '{{ .State.Running }}' apps-jellyfin)" = true
curl -fsS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed | grep -q '"catalog_id":"jellyfin"'`
