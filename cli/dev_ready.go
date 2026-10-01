// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"time"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

const (
	readyPollInterval = time.Second

	defaultHostAgentPort = "3000"
	defaultTraefikPort   = "8080"
)

// announceHostAgentReady watches the foreground host-agent from the side. The
// host-agent opens its API only after the first convergence pass (every
// installed app started and health-checked), which can take minutes and prints
// nothing useful in the meantime. This polls /api/health on the guest and
// prints a single ready step with the URLs once the API answers 200 (the
// host-agent answers 503 while it is still starting).
//
// While it waits it says so once, at progressEvery, and then only every
// devQuietProgressRest after that. The first wait is the one that surprises
// people; a note repeated on a fixed short cadence after that is the noise
// this console exists to remove, and a long wait stays visibly alive either
// way. It returns when ready or when ctx is canceled.
func announceHostAgentReady(ctx context.Context, ex executor.Executor, c *devConsole, ports map[string]string, poll, progressEvery time.Duration) {
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
			// The API port is already on the host-agent step above; the ready
			// line is the one a developer clicks, so it carries the UI origin
			// and nothing else.
			c.Blank()
			c.Step("ready", fmt.Sprintf("http://localhost:%s", uiPort), time.Since(start))
			return
		}

		if now := time.Now(); !now.Before(nextProgress) {
			c.Note(fmt.Sprintf("still starting apps (%s elapsed) · the UI opens when they are up",
				now.Sub(start).Round(time.Second)))
			nextProgress = now.Add(devQuietProgressRest)
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
