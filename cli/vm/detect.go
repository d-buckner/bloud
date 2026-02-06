package vm

import (
	"os"
	"runtime"
)

// RuntimeMode represents the detected execution environment
type RuntimeMode int

const (
	ModeLima   RuntimeMode = iota // macOS or non-NixOS Linux, use Lima VMs
	ModeNative                    // Native NixOS, run everything locally
)

var detectedMode = ModeLima

// DetectRuntime detects whether we're running on native NixOS or need Lima
func DetectRuntime() {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/run/current-system"); err == nil {
			detectedMode = ModeNative
			return
		}
	}
	detectedMode = ModeLima
}

// IsNative returns true if running on native NixOS
func IsNative() bool {
	return detectedMode == ModeNative
}

// GetMode returns the detected runtime mode
func GetMode() RuntimeMode {
	return detectedMode
}
