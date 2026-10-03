// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// cmdStart prints usage guidance: the real dev loop is ./bloud dev.
func cmdStart() int {
	name, err := backendName()
	if err != nil {
		errorf("No runtime backend: %v", err)
		return 1
	}

	fmt.Println("Start the dev environment:")
	fmt.Println()
	fmt.Println("  ./bloud dev          Build, deploy to runtime VM, and run host-agent (Ctrl-C to stop)")
	fmt.Println()
	switch name {
	case "qemu":
		fmt.Println("Prerequisites (QEMU backend):")
		fmt.Println("  ./bloud dev   # provisions .bloud/qemu/bloud-qemu, boots VM")
	case "native":
		fmt.Println("Prerequisites (native backend):")
		fmt.Println("  ./bloud setup  # checks prerequisites; ./bloud dev runs on this host")
	default:
		fmt.Println("Prerequisites (Lima backend):")
		fmt.Println("  limactl create --name=bloud-dev dev/lima.yaml")
		fmt.Println("  limactl start bloud-dev")
	}
	return 0
}

func cmdStop() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Stopping host-agent on " + inst)
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `pkill -f 'host-agent$' 2>/dev/null; systemctl --user stop apps-*.service 2>/dev/null; true`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stop host-agent: %v", err)
		return 1
	}
	log("Stopped")
	return 0
}

func cmdStatus() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	fmt.Println()
	fmt.Printf("  %s:  %s\n", vmLabel(name), inst)

	host := bk.Host()

	// Check if VM is running
	if host.Ready() {
		fmt.Printf("  VM status: %sRunning%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  VM status: %sStopped%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Start the VM with: ./bloud dev")
		return 0
	}

	// Check host-agent
	res, err := host.Executor().Run(context.Background(), executor.RunSpec{
		Command: `curl -sf http://localhost:3000/api/health 2>/dev/null && echo ok || echo down`,
	})
	if err == nil && strings.Contains(res.Stdout, "ok") {
		fmt.Printf("  Host agent: %sRunning%s (localhost:3000)\n", colorGreen, colorReset)
	} else {
		fmt.Printf("  Host agent: %sNot running%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run: ./bloud dev")
	}

	fmt.Println()
	return 0
}

func cmdLogs() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	log("Streaming host-agent logs from " + inst + " (Ctrl-C to stop)...")
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `journalctl --user -u host-agent -f 2>/dev/null || journalctl -f 2>/dev/null`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to stream logs: %v", err)
		return 1
	}
	return 0
}

func cmdAttach() int {
	bk, name, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	inst := vmInstance(name)
	sshex, ok := bk.Host().Executor().(*executor.SSHExecutor)
	if !ok {
		errorf("Backend host does not support interactive shells")
		return 1
	}
	log("Opening shell on " + inst + " (type 'exit' to leave)...")
	if err := sshex.InteractiveShell(context.Background(), os.Stdout, os.Stderr, os.Stdin); err != nil && !isSignalExit(err) {
		errorf("Failed to open shell on "+inst+": %v", err)
		return 1
	}
	return 0
}

func cmdShell(args []string) int {
	if len(args) == 0 {
		return cmdAttach()
	}
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	command := strings.Join(args, " ")
	if err := bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: command,
	}, os.Stdout, os.Stderr); err != nil && !isSignalExit(err) {
		errorf("Command failed: %v", err)
		return 1
	}
	return 0
}

func cmdRebuild() int {
	fmt.Println("'rebuild' is not supported (Nix runtime was removed).")
	fmt.Println()
	fmt.Println("To pick up code changes, re-run: ./bloud dev")
	return 0
}

func cmdServices() int {
	bk, _, err := devBackend()
	if err != nil {
		errorf("Could not set up backend: %v", err)
		return 1
	}
	err = bk.Host().Executor().RunStream(context.Background(), executor.RunSpec{
		Command: `podman ps --all --filter 'name=^apps-' --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'`,
	}, os.Stdout, os.Stderr)
	if err != nil && !isSignalExit(err) {
		errorf("Failed to list services: %v", err)
		return 1
	}
	return 0
}
