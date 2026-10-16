// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// The dashboard's HMR socket, routed around the host-agent.
//
// Vite's browser client reloads the page whenever its HMR socket drops. If that
// socket rides the host-agent proxy, then every backend reload drops it: the
// loop restarts host-agent on every watched Go save, and the reload throws away
// the store the frontend kernel works to keep alive across a hot swap.
//
// So vite puts the socket on a path of its own (`server.hmr.path` in
// services/host-agent/web/vite.config.ts) and this file writes the Traefik
// route that sends that path to vite directly. Traefik survives a host-agent
// restart and so does vite, because the loop restarts neither, so the socket
// stays up and a backend reload leaves the open page alone. The browser still
// only needs the one public port.
//
// Module requests keep riding the host-agent proxy. Each is one request, so a
// restart costs one failed fetch that the next save repeats; the socket is the
// part that cannot survive the process.
//
// The route is dev-only and it is its own file in the dynamic directory, so the
// orchestrator's generated `apps-routes.yml` stays the only product routing
// artifact. Traefik merges every file in that directory and `watch: true` picks
// this one up the moment it lands.
const (
	// devViteRouteFile is the file name in Traefik's dynamic directory.
	devViteRouteFile = "dev-vite.yml"
	// devViteHMRPath must match `server.hmr.path` in the web workspace's
	// vite.config.ts. TestDevViteRouteAgreesWithTheViteConfig fails the build
	// when the two drift apart.
	devViteHMRPath = "/bloud-hmr"
	// devViteRoutePriority only has to beat the `bloud-ui` catch-all, which
	// base.yml gives priority 1. The path is unique, so the number is there to
	// say "above the catch-all", not to win a contest.
	devViteRoutePriority = 97
)

// devViteRouteYAML renders the dynamic config for one dev loop.
//
// `passHostHeader: false` is deliberate. Vite answers the Host header through
// its allowedHosts guard, and the host in the browser's URL bar is not
// necessarily on that list. The host-agent proxy solves the same problem in its
// Director (see internal/api/dev_vite_proxy.go); this is the same rewrite, one
// hop earlier.
//
// The router names no entryPoint on purpose, so it applies to all of them. The
// static config only creates `web-local` when the canonical port is not already
// 8080, and the native backend runs on 8080 alone, so a named list would point
// at an entrypoint that does not exist.
func devViteRouteYAML(port int) string {
	return "# Written by `./bloud dev`. Dev-only route: it sends the dashboard's HMR\n" +
		"# socket straight to the vite dev server, so restarting host-agent for a\n" +
		"# backend change does not drop the socket and reload the page. Delete this\n" +
		"# file to put the socket back on the host-agent proxy.\n" +
		"http:\n" +
		"  routers:\n" +
		"    bloud-dev-vite:\n" +
		"      rule: \"PathPrefix(`" + devViteHMRPath + "`)\"\n" +
		"      service: bloud-dev-vite\n" +
		"      priority: " + strconv.Itoa(devViteRoutePriority) + "\n" +
		"  services:\n" +
		"    bloud-dev-vite:\n" +
		"      loadBalancer:\n" +
		"        passHostHeader: false\n" +
		"        servers:\n" +
		"          - url: \"http://" + devViteHost + ":" + strconv.Itoa(port) + "\"\n"
}

// writeDevViteRoute puts the route where Traefik watches for it.
func writeDevViteRoute(dynamicDir string, port int) error {
	if dynamicDir == "" {
		return fmt.Errorf("no Traefik dynamic directory to write the dev route into")
	}
	if err := os.MkdirAll(dynamicDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dynamicDir, err)
	}
	path := filepath.Join(dynamicDir, devViteRouteFile)
	if err := os.WriteFile(path, []byte(devViteRouteYAML(port)), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// removeDevViteRoute takes the route back out when the loop exits. A file that
// is already gone is the wanted state, so it is not an error. A run killed hard
// can leave the file behind; the next run rewrites it, and until then the only
// cost is one path that answers from a dev server that is not there.
func removeDevViteRoute(dynamicDir string) error {
	if dynamicDir == "" {
		return nil
	}
	path := filepath.Join(dynamicDir, devViteRouteFile)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
