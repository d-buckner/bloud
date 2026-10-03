// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/backend"
	"codeberg.org/d-buckner/bloud/cli/executor"
)

// parseYesFlag reads the confirmation-skipping -y/--yes flag out of a
// destructive command's args. Anything else is a typo, not a value to ignore:
// silently accepting an unknown flag on a command that deletes data means the
// flag you thought you passed (and the safety it was meant to carry) never
// took effect.
func parseYesFlag(cmdName string, args []string) (bool, error) {
	yes := false
	for _, arg := range args {
		switch arg {
		case "-y", "--yes":
			yes = true
		default:
			return false, fmt.Errorf("unknown %s flag %q (expected -y or --yes)", cmdName, arg)
		}
	}
	return yes, nil
}

// confirmDestructive asks a yes/no question before something destructive and
// reads the answer from r. Only a bare "y" (any case) confirms: an empty
// answer, a typo, or EOF (a non-interactive stdin) all abort, so a scripted
// run cannot wipe a runtime by default just because nobody was there to
// answer. Use -y/--yes where the answer is known in advance.
func confirmDestructive(w io.Writer, r *bufio.Reader, question string) bool {
	_, _ = fmt.Fprint(w, question)
	resp, _ := r.ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(resp), "y")
}

func cmdReset(args []string) int {
	yes, err := parseYesFlag("reset", args)
	if err != nil {
		errorf("%v", err)
		return 1
	}
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}

	if !yes {
		prompt := fmt.Sprintf("This will stop all services and wipe all app data in '%s'.\n"+
			"The VM itself is kept: only data, containers, and the database are removed.\n"+
			"Continue? [y/N] ", vmInstance(name))
		if !confirmDestructive(os.Stdout, stdinReader, prompt) {
			fmt.Println("Aborted.")
			return 0
		}
	}

	if err := resetRuntime(bk, name); err != nil {
		errorf("%v", err)
		return 1
	}

	log("Reset complete: run ./bloud dev to start fresh")
	return 0
}

// resetRuntime wipes the dev runtime: host-agent, app units, every container,
// and the data dir that holds the database. The VM itself is kept.
//
// Consent is the caller's job. cmdReset prompts; --reset on ./bloud dev is
// already an explicit request, so the same wipe runs unattended there.
func resetRuntime(bk backend.Backend, name string) error {
	host := bk.Host()
	ex := host.Executor()
	dirs := host.DataDirs()
	ctx := context.Background()

	// 1. Kill host-agent and any app systemd units
	log("Stopping host-agent")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop 'apps-*.service' 'bloud-e2e-host-agent.service' 2>/dev/null; true`,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to stop host-agent: %w", err)
	}

	// 2. Remove all containers
	log("Removing containers")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: `
set -e
podman rm -f $(podman ps -aq) 2>/dev/null || true
podman system prune -f 2>/dev/null || true
`,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to stop services: %w", err)
	}

	// 3. Wipe data directories and database. The runtime data dir is
	// backend-specific (Lima: /var/tmp/bloud-dev-runtime/data, QEMU:
	// /var/tmp/bloud-qemu-runtime/data); the bloud.db database lives inside
	// it (BLOUD_DATA_DIR). Use podman unshare for dirs with container-owned
	// files (e.g. postgres).
	//
	// $HOME/.local/share/bloud is a real, non-disposable path on native.
	homeWipe := ""
	if name != "native" {
		homeWipe = "podman unshare rm -rf \"$HOME/.local/share/bloud\"\n"
	}
	wipe := fmt.Sprintf("set -e\n%spodman unshare rm -rf %s\nrm -f %s/bloud.db\n", homeWipe, dirs.DataDir, dirs.DataDir)
	log("Wiping data")
	if err := ex.RunStream(ctx, executor.RunSpec{
		Command: wipe,
	}, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("failed to wipe data: %w", err)
	}

	return nil
}

func cmdDestroy() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Printf("This will stop and delete the %s '%s'.\n", vmLabel(name), inst)
	if !confirmDestructive(os.Stdout, stdinReader, "Continue? [y/N] ") {
		fmt.Println("Aborted.")
		return 0
	}
	if err := bk.Destroy(context.Background()); err != nil {
		errorf("Failed to delete VM: %v", err)
		return 1
	}
	log("VM deleted")
	return 0
}
