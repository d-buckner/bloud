// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"
)

// osReleasePath is where the host distribution metadata lives. A variable so
// tests can point it at a fixture instead of the real host.
var osReleasePath = "/etc/os-release"

// Release is the host distribution identity, read from /etc/os-release.
type Release struct {
	// ID is the distro identifier, lowercased ("fedora", "ubuntu", "arch").
	ID string
	// IDLike lists the distros this one derives from, lowercased.
	IDLike []string
}

// IsFedora reports whether the host is Fedora, either directly or through a
// derivative that names it in ID_LIKE.
func (r Release) IsFedora() bool {
	if r.ID == "fedora" {
		return true
	}
	for _, like := range r.IDLike {
		if like == "fedora" {
			return true
		}
	}
	return false
}

// loadRelease reads the distribution metadata from path. A missing or
// unreadable file yields an empty Release: an unknown host is not treated as
// any particular distro, so it is never blocked on a guess.
func loadRelease(path string) Release {
	f, err := os.Open(path)
	if err != nil {
		return Release{}
	}
	defer func() { _ = f.Close() }()

	var rel Release
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "ID":
			rel.ID = strings.ToLower(value)
		case "ID_LIKE":
			for _, like := range strings.Fields(value) {
				rel.IDLike = append(rel.IDLike, strings.ToLower(like))
			}
		}
	}
	return rel
}

// hostRelease returns the current host's distribution metadata.
func hostRelease() Release {
	return loadRelease(osReleasePath)
}

// nativeBlockedOn reports whether the native backend must be refused on this
// host, and why.
//
// The native backend runs host-agent directly on the machine it is invoked
// from: user-level systemd units, real port bindings, and containers on the
// developer's own rootless podman. There is no VM boundary. On Fedora that is
// unsupported and unsafe. A startup that fails partway leaves Bloud containers
// and systemd user units running against the developer's real host, where
// they were never meant to live, and tearing them down is not a clean
// operation. CI runs native on a throwaway runner, which is the only place
// that trade-off is acceptable.
func nativeBlockedOn(goos string, rel Release) (bool, string) {
	if goos == "linux" && rel.IsFedora() {
		return true, "the native backend is not supported on Fedora: it runs host-agent " +
			"directly on this host (systemd user units, real port bindings, rootless " +
			"podman) with no VM boundary, and a failed startup leaves containers and " +
			"units running on your machine. Use the qemu backend instead."
	}
	return false, ""
}

// checkBackend refuses a backend that must not run on this host. It is called
// on every resolution path, including the explicit BLOUD_BACKEND override,
// so a dangerous choice fails loudly instead of executing.
func checkBackend(name string, rel Release) error {
	blocked, reason := nativeBlockedOn(runtime.GOOS, rel)
	if blocked && name == "native" {
		return fmt.Errorf("%s", reason)
	}
	return nil
}
