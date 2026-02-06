package vm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PreflightError contains details about a failed preflight check
type PreflightError struct {
	Check      string
	Message    string
	FixCommand string
	FixURL     string
}

func (e *PreflightError) Error() string {
	return e.Message
}

// PreflightResult holds the results of all preflight checks
type PreflightResult struct {
	Errors []PreflightError
}

func (r *PreflightResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r *PreflightResult) AddError(check, message, fixCommand, fixURL string) {
	r.Errors = append(r.Errors, PreflightError{
		Check:      check,
		Message:    message,
		FixCommand: fixCommand,
		FixURL:     fixURL,
	})
}

// RunPreflightChecks runs all preflight checks and returns detailed results
func RunPreflightChecks(projectRoot string) *PreflightResult {
	result := &PreflightResult{}

	// Check Lima
	checkLima(result)

	// Check sshpass
	checkSshpass(result)

	// Check VM image
	checkVMImage(result, projectRoot)

	return result
}

// checkLima verifies Lima is installed
func checkLima(result *PreflightResult) {
	_, err := exec.LookPath("limactl")
	if err != nil {
		var fixCmd string
		if runtime.GOOS == "darwin" {
			fixCmd = "brew install lima"
		} else {
			fixCmd = "curl -fsSL https://lima-vm.io/install.sh | bash"
		}
		result.AddError(
			"lima",
			"Lima is not installed",
			fixCmd,
			"https://lima-vm.io/docs/installation/",
		)
	}
}

// checkSshpass verifies sshpass is installed (optional - only needed for password auth fallback)
func checkSshpass(result *PreflightResult) {
	// sshpass is now optional - we prefer SSH key auth
	// Only warn, don't add as error
}

// checkVMImage verifies the VM image exists
func checkVMImage(result *PreflightResult, projectRoot string) {
	imagePath := GetImagePath(projectRoot)
	if _, err := os.Stat(imagePath); err != nil {
		result.AddError(
			"vm-image",
			fmt.Sprintf("VM image not found at:\n    %s", imagePath),
			"",
			"",
		)
	}
}

// PrintPreflightErrors prints formatted preflight errors with fix instructions
func PrintPreflightErrors(result *PreflightResult) {
	fmt.Println()
	fmt.Println("\033[1;31m✗ Pre-flight checks failed\033[0m")
	fmt.Println()

	for i, err := range result.Errors {
		fmt.Printf("  \033[1;33m%d. %s\033[0m\n", i+1, err.Check)
		fmt.Printf("     %s\n", err.Message)
		if err.FixCommand != "" {
			fmt.Printf("     \033[36mFix:\033[0m %s\n", err.FixCommand)
		}
		if err.FixURL != "" {
			fmt.Printf("     \033[36mDocs:\033[0m %s\n", err.FixURL)
		}
		fmt.Println()
	}

	// Check if image is the issue and provide detailed help
	for _, err := range result.Errors {
		if err.Check == "vm-image" {
			PrintImageBuildInstructions()
			break
		}
	}
}

func PrintImageBuildInstructions() {
	fmt.Println("\033[1;36m━━━ How to get the VM image ━━━\033[0m")
	fmt.Println()

	arch := runtime.GOARCH
	if arch == "arm64" {
		arch = "aarch64"
	} else {
		arch = "x86_64"
	}

	fmt.Println("The VM image must be built on a Linux machine with Nix installed.")
	fmt.Println()
	fmt.Println("\033[1mOption 1: Build locally (if you have Nix on Linux)\033[0m")
	fmt.Println()
	fmt.Printf("  cd lima && ./build-image.sh --local\n")
	fmt.Println()
	fmt.Println("\033[1mOption 2: Build using a Lima Ubuntu VM\033[0m")
	fmt.Println()
	fmt.Println("  # First, create a temporary Ubuntu VM with Nix:")
	fmt.Println("  limactl start --name=nix-builder template://default")
	fmt.Println("  limactl shell nix-builder")
	fmt.Println()
	fmt.Println("  # Inside the VM, install Nix and build the image:")
	fmt.Printf("  curl -L https://nixos.org/nix/install | sh -s -- --daemon\n")
	fmt.Println("  . /etc/profile.d/nix.sh")
	fmt.Printf("  nix build github:kasuboski/nixos-lima#packages.%s-linux.img --extra-experimental-features 'nix-command flakes'\n", arch)
	fmt.Println("  cp $(readlink result)/nixos.img /tmp/lima/nixos-24.11-lima.img")
	fmt.Println()
	fmt.Println("  # Exit VM and copy image:")
	fmt.Println("  exit")
	fmt.Println("  mkdir -p lima/imgs")
	fmt.Println("  cp /tmp/lima/nixos-24.11-lima.img lima/imgs/")
	fmt.Println()
	fmt.Println("  # Clean up the builder VM:")
	fmt.Println("  limactl delete nix-builder --force")
	fmt.Println()
	fmt.Println("\033[2mNote: The image build takes 10-30 minutes depending on your machine.\033[0m")
	fmt.Println()
}

// RunNativePreflightChecks runs preflight checks for native NixOS
func RunNativePreflightChecks() *PreflightResult {
	result := &PreflightResult{}

	for _, tool := range []struct{ name, fixHint string }{
		{"go", "Ensure Go is available in your NixOS configuration"},
		{"node", "Ensure Node.js is available in your NixOS configuration"},
		{"tmux", "Ensure tmux is available in your NixOS configuration"},
		{"podman", "Ensure podman is available in your NixOS configuration"},
	} {
		if _, err := exec.LookPath(tool.name); err != nil {
			result.AddError(tool.name, tool.name+" is not installed", tool.fixHint, "")
		}
	}

	return result
}

// QuickPreflightCheck does a fast check and returns a simple error if anything fails
// Used for commands that just need to know if the basics are ready
func QuickPreflightCheck() error {
	if _, err := exec.LookPath("limactl"); err != nil {
		return fmt.Errorf("lima not installed")
	}
	if _, err := exec.LookPath("sshpass"); err != nil {
		return fmt.Errorf("sshpass not installed")
	}
	return nil
}

// GetImagePath returns the expected path to the VM image
func GetImagePath(projectRoot string) string {
	return filepath.Join(projectRoot, "lima", "imgs", "nixos-24.11-lima.img")
}

// ImageExists checks if the VM image exists
func ImageExists(projectRoot string) bool {
	imagePath := GetImagePath(projectRoot)
	_, err := os.Stat(imagePath)
	return err == nil
}

// ParseImagePathFromConfig extracts the image path from nixos.yaml
func ParseImagePathFromConfig(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}

	// Simple extraction - look for location: line under images:
	lines := strings.Split(string(data), "\n")
	inImages := false
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "images:") {
			inImages = true
			continue
		}
		if inImages && strings.Contains(line, "location:") {
			// Extract path from: - location: "~/Projects/bloud/lima/imgs/nixos-24.11-lima.img"
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				path := strings.TrimSpace(parts[1])
				path = strings.Trim(path, `"'`)
				// Expand ~
				if strings.HasPrefix(path, "~") {
					homeDir, _ := os.UserHomeDir()
					path = filepath.Join(homeDir, path[1:])
				}
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("could not find image path in config")
}
