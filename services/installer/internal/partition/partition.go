package partition

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func Prepare(ctx context.Context, device string, emit func(string)) error {
	// Unmount any partitions left over from a previous install attempt.
	// Errors are intentionally ignored — these may not be mounted.
	emit("Unmounting any existing mounts on " + device)
	exec.CommandContext(ctx, "umount", "-f", "/mnt/boot").Run() //nolint:errcheck
	exec.CommandContext(ctx, "umount", "-f", "/mnt").Run()       //nolint:errcheck

	steps := []struct {
		description string
		args        []string
	}{
		{
			"Clearing existing signatures from " + device,
			[]string{"wipefs", "-a", device},
		},
		{
			"Creating GPT partition table on " + device,
			[]string{"parted", "-s", device, "mklabel", "gpt"},
		},
		// Partition 1: BIOS boot — GRUB embeds its core image here for SeaBIOS/BIOS boot.
		// Required when grub.device is set (non-"nodev") on a GPT disk.
		{
			"Creating BIOS boot partition (1MiB–2MiB)",
			[]string{"parted", "-s", device, "mkpart", "BIOS", "1MiB", "2MiB"},
		},
		{
			"Setting bios_grub flag on partition 1",
			[]string{"parted", "-s", device, "set", "1", "bios_grub", "on"},
		},
		// Partition 2: EFI — for UEFI firmware (real hardware, OVMF-based VMs).
		{
			"Creating EFI partition (2MiB–514MiB)",
			[]string{"parted", "-s", device, "mkpart", "EFI", "fat32", "2MiB", "514MiB"},
		},
		{
			"Setting ESP flag on partition 2",
			[]string{"parted", "-s", device, "set", "2", "esp", "on"},
		},
		// Partition 3: root filesystem.
		{
			"Creating root partition (514MiB–100%)",
			[]string{"parted", "-s", device, "mkpart", "root", "ext4", "514MiB", "100%"},
		},
	}

	for _, step := range steps {
		emit(step.description)
		cmd := exec.CommandContext(ctx, step.args[0], step.args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", step.description, err, string(out))
		}
	}

	efi := partitionDevice(device, "2")
	root := partitionDevice(device, "3")

	emit("Formatting EFI partition " + efi + " as FAT32 (label: ESP)")
	if out, err := exec.CommandContext(ctx, "mkfs.vfat", "-F32", "-n", "ESP", efi).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.vfat %s: %w\n%s", efi, err, string(out))
	}

	emit("Formatting root partition " + root + " as ext4 (label: nixos)")
	if out, err := exec.CommandContext(ctx, "mkfs.ext4", "-F", "-L", "nixos", root).CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs.ext4 %s: %w\n%s", root, err, string(out))
	}

	emit("Creating mount point /mnt")
	if err := os.MkdirAll("/mnt", 0755); err != nil {
		return fmt.Errorf("mkdir /mnt: %w", err)
	}

	emit("Mounting root partition " + root + " at /mnt")
	if out, err := exec.CommandContext(ctx, "mount", root, "/mnt").CombinedOutput(); err != nil {
		return fmt.Errorf("mounting root: %w\n%s", err, string(out))
	}

	emit("Creating /mnt/boot directory")
	if err := os.MkdirAll("/mnt/boot", 0755); err != nil {
		return fmt.Errorf("mkdir /mnt/boot: %w", err)
	}

	emit("Mounting EFI partition " + efi + " at /mnt/boot")
	if out, err := exec.CommandContext(ctx, "mount", efi, "/mnt/boot").CombinedOutput(); err != nil {
		return fmt.Errorf("mounting EFI: %w\n%s", err, string(out))
	}

	return nil
}

func partitionDevice(device, suffix string) string {
	if strings.Contains(device, "nvme") || strings.Contains(device, "mmcblk") {
		return device + "p" + suffix
	}
	return device + suffix
}

func runWithOutput(ctx context.Context, emit func(string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return err
	}

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		emit(scanner.Text())
	}

	return cmd.Wait()
}

var _ = runWithOutput
