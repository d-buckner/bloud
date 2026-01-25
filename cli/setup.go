package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/cli/vm"
)

const (
	// GitHub release URL pattern for pre-built images
	imageReleaseURL = "https://github.com/d-buckner/bloud/releases/latest/download"
)

func cmdSetup() int {
	fmt.Println()
	fmt.Printf("%s╭─────────────────────────────────────╮%s\n", colorCyan, colorReset)
	fmt.Printf("%s│       Bloud Development Setup       │%s\n", colorCyan, colorReset)
	fmt.Printf("%s╰─────────────────────────────────────╯%s\n", colorCyan, colorReset)
	fmt.Println()

	projectRoot, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Check each prerequisite
	allGood := true

	// 1. Check Lima
	fmt.Print("  Checking Lima...              ")
	if checkCommand("limactl") {
		fmt.Printf("%s✓ installed%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s✗ not installed%s\n", colorRed, colorReset)
		printInstallHint("lima")
		allGood = false
	}

	// 2. Check sshpass (optional - only needed if SSH key auth fails)
	fmt.Print("  Checking sshpass...           ")
	if checkCommand("sshpass") {
		fmt.Printf("%s✓ installed%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s○ not installed (optional)%s\n", colorYellow, colorReset)
		fmt.Printf("     SSH key auth will be used instead. sshpass is only needed\n")
		fmt.Printf("     as a fallback if SSH keys aren't configured.\n")
		// Don't fail - sshpass is optional now
	}

	// 3. Check VM image
	fmt.Print("  Checking VM image...    ")
	imagePath := vm.GetImagePath(projectRoot)
	if vm.ImageExists(projectRoot) {
		fmt.Printf("%s✓ found%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s✗ not found%s\n", colorYellow, colorReset)
		fmt.Printf("     Expected at: %s\n", imagePath)

		// Offer to download
		if offerImageDownload(projectRoot) {
			// Re-check after download
			if vm.ImageExists(projectRoot) {
				fmt.Printf("\n  VM image...             %s✓ downloaded%s\n", colorGreen, colorReset)
			} else {
				allGood = false
			}
		} else {
			allGood = false
		}
	}

	fmt.Println()

	if !allGood {
		fmt.Printf("%s✗ Some prerequisites are missing.%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Fix the issues above, then run './bloud setup' again.")
		fmt.Println()
		return 1
	}

	// 4. Create VM if it doesn't exist
	fmt.Print("  Checking VM...                ")
	vmExists := vm.Exists(devVMName)
	vmRunning := vm.IsRunning(devVMName)

	if vmExists && vmRunning {
		fmt.Printf("%s✓ running%s\n", colorGreen, colorReset)
	} else if vmExists {
		fmt.Printf("%s○ stopped%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Starting VM...")
		if err := vm.Start(devVMName); err != nil {
			errorf("Failed to start VM: %v", err)
			return 1
		}
		// Wait for SSH
		if err := waitForVMReady(devVMName); err != nil {
			errorf("VM failed to become ready: %v", err)
			return 1
		}
	} else {
		fmt.Printf("%s○ not created%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Creating VM (this may take a minute)...")

		configPath := filepath.Join(projectRoot, "lima", "nixos.yaml")
		if err := vm.Create(devVMName, configPath); err != nil {
			errorf("Failed to create VM: %v", err)
			return 1
		}
		// Wait for SSH
		if err := waitForVMReady(devVMName); err != nil {
			errorf("VM failed to become ready: %v", err)
			return 1
		}
		fmt.Printf("  VM created:                   %s✓ running%s\n", colorGreen, colorReset)
	}

	// 5. Mount filesystems so flake is accessible
	mounts := []vm.Mount{
		{Tag: "mount0", MountPath: devProjectInVM},
		{Tag: "mount1", MountPath: "/tmp/lima"},
	}
	if err := vm.MountFilesystems(devVMName, mounts); err != nil {
		warn(fmt.Sprintf("Mount warning: %v", err))
	}

	// 6. Check if bloud module is applied (bloud-apps.target exists)
	fmt.Print("  Checking NixOS config...      ")
	output, err := vm.Exec(devVMName, "systemctl --user cat bloud-apps.target 2>/dev/null")
	needsRebuild := err != nil || !strings.Contains(output, "[Unit]")

	if needsRebuild {
		fmt.Printf("%s○ needs update%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Rebuilding NixOS configuration (this may take a few minutes)...")
		fmt.Println()
		// Set git safe.directory for both user and root (sudo runs as root)
		// This is needed for older images that don't have it in /etc/gitconfig
		_, _ = vm.Exec(devVMName, "git config --global --add safe.directory '*'")
		_, _ = vm.Exec(devVMName, "sudo git config --global --add safe.directory '*'")
		rebuildCmd := fmt.Sprintf("sudo nixos-rebuild switch --flake %s#vm-dev --impure", devProjectInVM)
		if err := vm.ExecStream(devVMName, rebuildCmd); err != nil {
			errorf("NixOS rebuild failed: %v", err)
			return 1
		}
		fmt.Println()
		fmt.Printf("  NixOS config:                 %s✓ rebuilt%s\n", colorGreen, colorReset)
	} else {
		fmt.Printf("%s✓ up to date%s\n", colorGreen, colorReset)
	}

	fmt.Println()
	fmt.Printf("%s✓ Setup complete!%s\n", colorGreen, colorReset)
	fmt.Println()
	fmt.Println("  Run './bloud start' to start the development environment.")
	fmt.Println()
	return 0
}

func checkCommand(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

func printInstallHint(tool string) {
	switch tool {
	case "lima":
		if runtime.GOOS == "darwin" {
			fmt.Printf("     Fix: %sbrew install lima%s\n", colorCyan, colorReset)
		} else {
			fmt.Printf("     Fix: %scurl -fsSL https://lima-vm.io/install.sh | bash%s\n", colorCyan, colorReset)
		}
	case "sshpass":
		if runtime.GOOS == "darwin" {
			fmt.Printf("     Fix: %sbrew install hudochenkov/sshpass/sshpass%s\n", colorCyan, colorReset)
		} else {
			fmt.Printf("     Fix: %ssudo apt install sshpass%s\n", colorCyan, colorReset)
		}
	}
}

func offerImageDownload(projectRoot string) bool {
	fmt.Println()

	// Determine architecture
	arch := runtime.GOARCH
	if arch == "arm64" {
		arch = "aarch64"
	} else if arch == "amd64" {
		arch = "x86_64"
	}

	imageFilename := fmt.Sprintf("nixos-24.11-%s.img.gz", arch)
	downloadURL := fmt.Sprintf("%s/%s", imageReleaseURL, imageFilename)

	fmt.Printf("  %sWould you like to download the pre-built image?%s\n", colorYellow, colorReset)
	fmt.Printf("  Architecture: %s\n", arch)
	fmt.Printf("  Size: ~2.5 GB compressed, ~7 GB extracted\n")
	fmt.Println()

	// Check if release exists first
	fmt.Print("  Checking for pre-built image... ")
	resp, err := http.Head(downloadURL)
	if err != nil || resp.StatusCode != 200 {
		fmt.Printf("%snot available%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Pre-built images are not yet available for download.")
		fmt.Println("  You'll need to build the image manually.")
		fmt.Println()
		vm.PrintImageBuildInstructions()
		return false
	}
	resp.Body.Close()
	fmt.Printf("%savailable%s\n", colorGreen, colorReset)

	fmt.Println()
	fmt.Printf("  Download from: %s\n", downloadURL)
	fmt.Println()
	fmt.Print("  Download now? [Y/n]: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(strings.ToLower(input))

	if input != "" && input != "y" && input != "yes" {
		fmt.Println()
		fmt.Println("  Skipping download. You can build the image manually:")
		fmt.Println()
		vm.PrintImageBuildInstructions()
		return false
	}

	// Download the image
	return downloadImage(downloadURL, projectRoot, arch)
}

func downloadImage(url, projectRoot, arch string) bool {
	fmt.Println()
	fmt.Println("  Downloading VM image...")

	// Create imgs directory
	imgsDir := filepath.Join(projectRoot, "lima", "imgs")
	if err := os.MkdirAll(imgsDir, 0755); err != nil {
		errorf("Failed to create directory: %v", err)
		return false
	}

	// Download to temp file
	tmpFile := filepath.Join(imgsDir, "nixos-download.img.gz.tmp")
	finalFile := filepath.Join(imgsDir, "nixos-24.11-lima.img")

	// Start download
	resp, err := http.Get(url)
	if err != nil {
		errorf("Failed to download: %v", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errorf("Download failed: HTTP %d", resp.StatusCode)
		return false
	}

	// Get content length for progress
	contentLength := resp.ContentLength

	// Create temp file
	out, err := os.Create(tmpFile)
	if err != nil {
		errorf("Failed to create temp file: %v", err)
		return false
	}

	// Download with progress
	written, err := copyWithProgress(out, resp.Body, contentLength, "  Downloading")
	out.Close()
	if err != nil {
		os.Remove(tmpFile)
		errorf("Download failed: %v", err)
		return false
	}

	fmt.Printf("\n  Downloaded %.1f MB\n", float64(written)/(1024*1024))

	// Extract gzipped image
	fmt.Println()
	fmt.Println("  Extracting image...")

	gzFile, err := os.Open(tmpFile)
	if err != nil {
		os.Remove(tmpFile)
		errorf("Failed to open downloaded file: %v", err)
		return false
	}

	gzReader, err := gzip.NewReader(gzFile)
	if err != nil {
		gzFile.Close()
		os.Remove(tmpFile)
		errorf("Failed to decompress: %v", err)
		return false
	}

	imgFile, err := os.Create(finalFile)
	if err != nil {
		gzReader.Close()
		gzFile.Close()
		os.Remove(tmpFile)
		errorf("Failed to create image file: %v", err)
		return false
	}

	// Extract with progress (estimate ~7GB uncompressed)
	estimatedSize := int64(7 * 1024 * 1024 * 1024)
	_, err = copyWithProgress(imgFile, gzReader, estimatedSize, "  Extracting")
	imgFile.Close()
	gzReader.Close()
	gzFile.Close()

	if err != nil {
		os.Remove(tmpFile)
		os.Remove(finalFile)
		errorf("Extraction failed: %v", err)
		return false
	}

	// Clean up temp file
	os.Remove(tmpFile)

	fmt.Println()
	fmt.Printf("  %s✓ Image extracted to:%s %s\n", colorGreen, colorReset, finalFile)

	return true
}

func copyWithProgress(dst io.Writer, src io.Reader, total int64, prefix string) (int64, error) {
	var written int64
	buf := make([]byte, 32*1024)
	lastPercent := -1

	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				written += int64(nw)
			}
			if ew != nil {
				return written, ew
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			return written, er
		}

		// Update progress
		if total > 0 {
			percent := int(written * 100 / total)
			if percent != lastPercent {
				bar := progressBar(percent, 30)
				fmt.Printf("\r%s: %s %3d%%", prefix, bar, percent)
				lastPercent = percent
			}
		} else {
			// Unknown size - just show bytes
			fmt.Printf("\r%s: %.1f MB", prefix, float64(written)/(1024*1024))
		}
	}

	return written, nil
}

func progressBar(percent, width int) string {
	filled := width * percent / 100
	empty := width - filled

	bar := strings.Repeat("█", filled) + strings.Repeat("░", empty)
	return bar
}

// waitForVMReady waits for the VM to boot and SSH to become available
func waitForVMReady(vmName string) error {
	ctx := context.Background()

	// Give QEMU time to start
	time.Sleep(5 * time.Second)

	// Wait for SSH
	if err := vm.WaitForSSH(ctx, vmName, 2*time.Minute); err != nil {
		return err
	}

	return nil
}
