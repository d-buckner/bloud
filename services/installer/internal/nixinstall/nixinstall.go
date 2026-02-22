package nixinstall

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
)

func Install(ctx context.Context, flakePath string, emit func(string)) error {
	if flakePath == "" {
		flakePath = os.Getenv("INSTALLER_FLAKE_PATH")
	}
	if flakePath == "" {
		flakePath = "/etc/bloud"
	}

	if err := writeConfigStub(); err != nil {
		return fmt.Errorf("writing nixos config stub: %w", err)
	}

	emit("Running nixos-install --no-root-passwd --flake " + flakePath + "#bloud --root /mnt")

	cmd := exec.CommandContext(ctx,
		"/run/current-system/sw/bin/nixos-install",
		"--no-root-passwd",
		"--flake", flakePath+"#bloud",
		"--root", "/mnt",
	)

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
