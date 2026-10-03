// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"codeberg.org/d-buckner/bloud/cli/backend"
)

// localExec runs a command on the host machine
func localExec(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// isSignalExit reports whether err came from a process killed by a signal
// (e.g. Ctrl-C), which interactive commands treat as a clean stop.
func isSignalExit(err error) bool {
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == -1 {
		return true
	}
	return false
}

// rootMarkers are files whose presence identifies the Bloud repo root. They are
// deliberately root-level and stable: keying off a single documentation file
// breaks root resolution every time the docs tree is reorganized.
var rootMarkers = []string{"validation.yaml", "AGENTS.md", "docs/specs/spec.md"}

func getProjectRoot() (string, error) {
	// Find project root by looking for cli/main.go relative to executable or cwd
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Check if we're in the project root
	if _, err := os.Stat(filepath.Join(cwd, "cli", "main.go")); err == nil {
		return cwd, nil
	}

	// Check if we're in cli directory
	if _, err := os.Stat(filepath.Join(cwd, "main.go")); err == nil {
		return filepath.Dir(cwd), nil
	}

	// Walk up looking for a root marker.
	for dir := cwd; dir != "/"; dir = filepath.Dir(dir) {
		for _, marker := range rootMarkers {
			if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
				return dir, nil
			}
		}
	}

	return "", fmt.Errorf("could not find project root (looking for %s)", strings.Join(rootMarkers, ", "))
}

func limaInstance() string {
	if v := os.Getenv("BLOUD_E2E_LIMA_INSTANCE"); v != "" {
		return v
	}
	return "bloud-dev"
}

func qemuInstance() string {
	if v := os.Getenv("BLOUD_QEMU_INSTANCE"); v != "" {
		return v
	}
	return "bloud-qemu"
}

// vmInstance returns the instance name for a backend. Native backends have
// no instance; the label is "native".
func vmInstance(name string) string {
	switch name {
	case "qemu":
		return qemuInstance()
	case "native":
		return "native"
	default:
		return limaInstance()
	}
}

// trustedLocalNetsEnv returns the BLOUD_TRUSTED_LOCAL_NETS value for the
// host-agent. QEMU slirp NAT presents host-forwarded connections from the
// gateway (10.0.2.2), not loopback, so the host-agent must trust that subnet
// for host-side API calls (e.g. ./bloud install, e2e). Lima forwards to
// loopback, so no trusted nets are needed.
func trustedLocalNetsEnv(name string) string {
	if name == "qemu" {
		return "10.0.2.0/24"
	}
	return ""
}

// traefikPortEnv returns the BLOUD_TRAEFIK_PORT override for a backend. Traefik
// defaults to its canonical :80 entrypoint; the native backend runs unprivileged
// (no root, and port 80 may be occupied on the developer machine), so it keeps
// the always-bound 8080 compat entrypoint. The VM backends serve :80 and expose
// it on the host as 8080, so they leave the default in place.
func traefikPortEnv(name string) string {
	if name == "native" {
		return "8080"
	}
	return ""
}

// ssoIssuerURL is the OIDC issuer base URL reachable from inside app
// containers: the SSO subdomain of the base domain, mapped to the host
// gateway via per-app extraHosts. An explicit BLOUD_SSO_ISSUER_URL wins.
func ssoIssuerURL() string {
	if v := os.Getenv("BLOUD_SSO_ISSUER_URL"); v != "" {
		return v
	}
	baseDomain := os.Getenv("BLOUD_BASE_DOMAIN")
	if baseDomain == "" {
		baseDomain = "localhost"
	}
	return fmt.Sprintf("http://sso.%s:8080", baseDomain)
}

// vmLabel is the human-readable backend name for a backend.
func vmLabel(name string) string {
	switch name {
	case "qemu":
		return "QEMU VM"
	case "native":
		return "native host"
	default:
		return "Lima VM"
	}
}

// devBackend builds the selected backend for the current project and
// returns its name.
func devBackend() (backend.Backend, string, error) {
	root, err := getProjectRoot()
	if err != nil {
		return nil, "", err
	}
	name, err := backendName()
	if err != nil {
		return nil, "", err
	}
	switch name {
	case "qemu":
		return backend.NewQEMUBackend(qemuInstance(), root), name, nil
	case "native":
		return backend.NewNativeBackend(root), name, nil
	default:
		return backend.NewLimaBackend(limaInstance(), root), name, nil
	}
}
