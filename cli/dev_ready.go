// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

const (
	readyPollInterval     = time.Second
	readyProgressInterval = 15 * time.Second

	defaultHostAgentPort = "3000"
	defaultTraefikPort   = "8080"
)

// announceHostAgentReady watches the foreground host-agent from the side. The
// host-agent opens its API only after the first convergence pass (every
// installed app started and health-checked), which can take minutes and prints
// nothing useful in the meantime. This polls /api/health on the guest, prints a
// progress line every progressEvery while waiting, and a ready line with the
// URLs once the API answers 200 (the host-agent answers 503 while it is still
// starting). It returns when ready or when ctx is cancelled.
func announceHostAgentReady(ctx context.Context, ex executor.Executor, out io.Writer, ports map[string]string, poll, progressEvery time.Duration) {
	apiPort := portOr(ports, "host-agent", defaultHostAgentPort)
	uiPort := portOr(ports, "traefik", defaultTraefikPort)
	healthCmd := fmt.Sprintf("curl -sf -o /dev/null -m 2 http://localhost:%s/api/health", apiPort)

	start := time.Now()
	nextProgress := start.Add(progressEvery)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		res, err := ex.Run(ctx, executor.RunSpec{Command: healthCmd})
		if ctx.Err() != nil {
			return
		}
		if err == nil && res.ExitCode == 0 {
			fprintLog(out, fmt.Sprintf("Bloud is ready: http://localhost:%s (API: http://localhost:%s). Ctrl-C to stop.", uiPort, apiPort))
			return
		}

		if now := time.Now(); !now.Before(nextProgress) {
			fprintLog(out, fmt.Sprintf("Still starting apps, the UI opens when they are up (%s elapsed)", now.Sub(start).Round(time.Second)))
			nextProgress = now.Add(progressEvery)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// portOr returns ports[name], or fallback when the backend does not report it.
func portOr(ports map[string]string, name, fallback string) string {
	if p := ports[name]; p != "" {
		return p
	}
	return fallback
}
