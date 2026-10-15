// SPDX-License-Identifier: AGPL-3.0-only

package authentik

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	authentikClient "codeberg.org/d-buckner/bloud/pkg/authentik"
)

const (
	// authFlowFile is the copy of auth.yaml PreStart writes into the app's data
	// directory, which the server and worker containers bind-mount.
	authFlowFile = "authentik-auth-flow.yaml"
	// authFlowBlueprintPath is where that file is mounted, relative to the
	// worker's /blueprints directory: over the upstream default flow's file.
	authFlowBlueprintPath = "default/flow-default-authentication-flow.yaml"

	workerNode = "apps-authentik-worker"

	// A restarted worker applies changed blueprints during startup, measured at
	// about 16s on a dev instance. The budget leaves room for a slow host while
	// staying well inside the framework's PostStart budget.
	blueprintApplyTimeout = 2 * time.Minute
	blueprintApplyPoll    = 2 * time.Second
)

// ensureAuthFlowApplied makes sure Authentik's worker has applied the auth flow
// blueprint PreStart wrote, and returns once it has.
//
// The worker is the container that applies blueprints, and it does not see a
// rewrite of the file on its own. managedfile.Write replaces the file
// atomically, which gives it a new inode, while a single-file bind mount stays
// on the inode it was started with. The worker therefore keeps reading the old
// content until it is restarted: its file watcher never fires, and the hourly
// discovery re-reads the stale copy. PreStart's restart covers the server only.
//
// The check is a diff against what Authentik reports, so it also repairs an
// install whose worker missed an earlier rewrite. Steady state is one GET:
// Authentik records the sha512 of the content it last applied.
func (c *ServerConfigurator) ensureAuthFlowApplied(ctx context.Context, client *authentikClient.Client, dataPath string) error {
	content, err := os.ReadFile(filepath.Join(dataPath, authFlowFile))
	if err != nil {
		return fmt.Errorf("read auth flow blueprint: %w", err)
	}
	sum := sha512.Sum512(content)
	want := hex.EncodeToString(sum[:])

	applied, err := client.BlueprintApplied(ctx, authFlowBlueprintPath)
	if err != nil {
		return err
	}
	if applied.Hash == want {
		return nil
	}

	// A worker that sees the current content is still applying it (a cold
	// install, or a restart already under way), so it is only waited on. One
	// that sees an older file is on a stale mount and needs the restart.
	mounted, err := c.workerBlueprintHash(ctx)
	if err != nil {
		return err
	}
	if mounted != want {
		if c.deps.Runtime.RestartContainer == nil {
			return fmt.Errorf("auth flow blueprint changed but no container restart is available")
		}
		c.logger.Info("restarting the Authentik worker to apply the rewritten auth flow blueprint")
		if err := c.deps.Runtime.RestartContainer(ctx, workerNode); err != nil {
			return fmt.Errorf("restart %s: %w", workerNode, err)
		}
	}
	return waitForBlueprint(ctx, client, want)
}

// workerBlueprintHash returns the sha512 of the auth flow blueprint as the
// worker container sees it through its mount.
func (c *ServerConfigurator) workerBlueprintHash(ctx context.Context) (string, error) {
	if c.deps.Runtime.Exec == nil {
		return "", fmt.Errorf("no container exec available (no host runtime wired)")
	}
	out, err := c.deps.Runtime.Exec(ctx, workerNode, nil, []string{"sha512sum", "/blueprints/" + authFlowBlueprintPath})
	if err != nil {
		return "", fmt.Errorf("hash the worker's auth flow blueprint: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", fmt.Errorf("hash the worker's auth flow blueprint: empty output")
	}
	return fields[0], nil
}

// waitForBlueprint polls until Authentik reports the auth flow blueprint applied
// at the wanted content.
func waitForBlueprint(ctx context.Context, client *authentikClient.Client, want string) error {
	deadline := time.Now().Add(blueprintApplyTimeout)
	for {
		applied, err := client.BlueprintApplied(ctx, authFlowBlueprintPath)
		if err == nil && applied.Hash == want {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("waiting for the auth flow blueprint to apply: %w", err)
			}
			return fmt.Errorf("auth flow blueprint not applied within %s (status %q)", blueprintApplyTimeout, applied.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(blueprintApplyPoll):
		}
	}
}
