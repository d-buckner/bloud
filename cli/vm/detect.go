package vm

import (
	"os"
	"runtime"
)

var nativeMode = false

// DetectRuntime detects whether we're running on native NixOS
func DetectRuntime() {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/run/current-system"); err == nil {
			nativeMode = true
		}
	}
}

// IsNative returns true if running on native NixOS
func IsNative() bool {
	return nativeMode
}
