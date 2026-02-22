package nixinstall

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
)

func Install(ctx context.Context, flakePath string, emit func(string)) error {
	if err := writeConfigStub(); err != nil {
		return fmt.Errorf("writing nixos config stub: %w", err)
	}

	var args []string
	// Prefer a pre-built system path when available. The bundled flake evaluates
	// to a different store hash than what's baked into the ISO squashfs, so
	// --flake re-evaluation would fail with a missing store path.
	if systemPath := os.Getenv("INSTALLER_SYSTEM_PATH"); systemPath != "" {
		emit("Running nixos-install --no-root-passwd --system " + systemPath + " --root /mnt")
		args = []string{"--no-root-passwd", "--system", systemPath, "--root", "/mnt"}
	} else {
		if flakePath == "" {
			flakePath = os.Getenv("INSTALLER_FLAKE_PATH")
		}
		if flakePath == "" {
			flakePath = "/etc/bloud"
		}
		emit("Running nixos-install --no-root-passwd --flake " + flakePath + "#bloud --root /mnt")
		args = []string{"--no-root-passwd", "--flake", flakePath + "#bloud", "--root", "/mnt"}
	}

	cmd := exec.CommandContext(ctx, "/run/current-system/sw/bin/nixos-install", args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("creating stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting nixos-install: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		emit(scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("nixos-install: %w", err)
	}

	return nil
}

func writeConfigStub() error {
	if err := os.MkdirAll("/mnt/etc/nixos", 0755); err != nil {
		return err
	}

	const stub = `{ ... }:
{
  imports = [ ];
}
`
	return os.WriteFile("/mnt/etc/nixos/configuration.nix", []byte(stub), 0644)
}
