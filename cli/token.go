// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package main

import (
	"context"
	"fmt"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// runtimeAPIToken reads the host-agent API bearer token by running
// `host-agent token <dataDir>` through the runtime host's Executor. Because
// the read rides the same Executor seam the CLI already uses for every
// backend — limactl shell, ssh, or local — it resolves uniformly across
// Lima, QEMU, and native without the CLI knowing where secrets.json lives on
// each. The host-agent binary is always at DataDirs.HostAgentDir/host-agent
// and the runtime data dir at DataDirs.DataDir (the unit's BLOUD_DATA_DIR).
//
// The loopback auto-admin bypass was removed: the CLI can no longer rely on
// network position for admin, so every protected call (install, uninstall,
// the e2e harness) presents this token instead.
func runtimeAPIToken(h executor.Host) (string, error) {
	dirs := h.DataDirs()
	bin := dirs.HostAgentDir + "/host-agent"
	res, err := h.Executor().Run(context.Background(), executor.RunSpec{
		Command: shellQuote(bin) + " token " + shellQuote(dirs.DataDir),
	})
	if err != nil {
		return "", fmt.Errorf("reading API token: %w: %s", err, strings.TrimSpace(res.Stderr))
	}
	token := strings.TrimSpace(res.Stdout)
	if token == "" {
		return "", fmt.Errorf("host-agent returned an empty API token")
	}
	return token, nil
}

// cmdToken implements `bloud token`: print the running instance's API
// bearer token so operators and the e2e harness can authenticate against
// the host-agent API.
func cmdToken() int {
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	token, err := runtimeAPIToken(bk.Host())
	if err != nil {
		errorf("%v", err)
		return 1
	}
	fmt.Println(token)
	return 0
}
