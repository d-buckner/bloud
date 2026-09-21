// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
)

func (r *lifecycle) collectLogs() {
	if r.cfg.remoteHome == "" {
		return
	}
	if err := os.MkdirAll(r.artifactDir(), 0755); err != nil {
		errorf("failed to create artifact dir: %v", err)
		return
	}
	logs := map[string]string{
		"journal.log": `journalctl --user -u bloud-e2e-host-agent.service --no-pager -n 500 || true`,
		"podman.log":  `podman ps -a; podman inspect apps-jellyfin 2>&1 || true`,
		"routes.log":  `test -f "$1/apps-routes.yml" && cat "$1/apps-routes.yml"; true`,
	}
	for name, script := range logs {
		args := []string{}
		if name == "routes.log" {
			args = append(args, r.cfg.traefikDir)
		}
		output, err := r.remoteOutput(script, args...)
		if err != nil {
			output += "\n" + err.Error()
		}
		if err := os.WriteFile(filepath.Join(r.artifactDir(), name), []byte(output), 0644); err != nil {
			errorf("failed to write %s artifact: %v", name, err)
		}
	}
}

func (r *lifecycle) cleanupRemoteDeployment() {
	script := `tok="$(cat ${3}/data/host-agent-api-token)"
curl -fsS -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall >/dev/null 2>&1 || true
podman rm -f apps-jellyfin >/dev/null 2>&1 || true
systemctl --user disable --now "$1" >/dev/null 2>&1 || true
rm -f "$2/.config/systemd/user/$1"
if test -f "$3/.bloud-e2e-runtime"; then
  rm -rf "$3"
fi
systemctl --user daemon-reload >/dev/null 2>&1 || true`
	if err := r.remoteRun(script, lifecycleHostAgentUnit, r.cfg.remoteHome, r.cfg.remoteDir); err != nil {
		errorf("failed to clean up remote deployment: %v", err)
	}
}

var remoteUninstallScript = `tok="$(cat ${1}/data/host-agent-api-token)"
http_code="$(curl -sS -o /dev/null -w '%%{http_code}' -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"clearData":true}' http://localhost:3000/api/apps/jellyfin/uninstall)"
printf 'uninstall response: %%s\n' "$http_code"
test "$http_code" -ge 200 && test "$http_code" -lt 300
deadline=$((SECONDS + 300))
until ! curl -sS -H "Authorization: Bearer $tok" http://localhost:3000/api/apps/installed | grep -q '"catalog_id":"jellyfin"'; do
  if ((SECONDS >= deadline)); then echo "timed out waiting for jellyfin removal"; exit 1; fi
  sleep 2
done
! podman container exists apps-jellyfin
test ! -e "$1/data/jellyfin"
! grep -q 'jellyfin' "$2/apps-routes.yml"`
