// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// devViteRouteDoc mirrors the part of Traefik's dynamic schema this file emits.
type devViteRouteDoc struct {
	HTTP struct {
		Routers map[string]struct {
			Rule     string `yaml:"rule"`
			Service  string `yaml:"service"`
			Priority int    `yaml:"priority"`
		} `yaml:"routers"`
		Services map[string]struct {
			LoadBalancer struct {
				PassHostHeader *bool `yaml:"passHostHeader"`
				Servers        []struct {
					URL string `yaml:"url"`
				} `yaml:"servers"`
			} `yaml:"loadBalancer"`
		} `yaml:"services"`
	} `yaml:"http"`
}

func TestDevViteRouteYAMLIsWhatTraefikReads(t *testing.T) {
	var doc devViteRouteDoc
	if err := yaml.Unmarshal([]byte(devViteRouteYAML(devVitePort)), &doc); err != nil {
		t.Fatalf("traefik could not read the generated route: %v", err)
	}

	router, ok := doc.HTTP.Routers["bloud-dev-vite"]
	if !ok {
		t.Fatalf("no bloud-dev-vite router in:\n%s", devViteRouteYAML(devVitePort))
	}
	if !strings.Contains(router.Rule, devViteHMRPath) {
		t.Errorf("router rule %q must match the HMR path %q", router.Rule, devViteHMRPath)
	}
	// base.yml gives the bloud-ui catch-all priority 1. A router that does not
	// beat it never sees the socket.
	if router.Priority <= 1 {
		t.Errorf("router priority %d must beat the catch-all's 1", router.Priority)
	}

	service, ok := doc.HTTP.Services[router.Service]
	if !ok {
		t.Fatalf("router names service %q, which the file does not define", router.Service)
	}
	if len(service.LoadBalancer.Servers) != 1 {
		t.Fatalf("expected one server, got %d", len(service.LoadBalancer.Servers))
	}
	want := "http://" + devViteHost + ":" + strconv.Itoa(devVitePort)
	if got := service.LoadBalancer.Servers[0].URL; got != want {
		t.Errorf("vite is at %s, route points at %q", want, got)
	}
	// Traefik must not pass the browser's Host header through: vite answers it
	// through allowedHosts, and the public host is not on that list.
	if service.LoadBalancer.PassHostHeader == nil || *service.LoadBalancer.PassHostHeader {
		t.Error("passHostHeader must be false so vite does not reject the upgrade")
	}
}

// The path and the port each live in two languages. This is the guard that keeps
// the Go side and the vite side describing the same socket.
func TestDevViteRouteAgreesWithTheViteConfig(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("find the project root: %v", err)
	}
	config := readRepoFile(t, root, "services", "host-agent", "web", "vite.config.ts")
	manifest := readRepoFile(t, root, "services", "host-agent", "web", "package.json")

	vitePath := strings.TrimPrefix(devViteHMRPath, "/")
	if !strings.Contains(config, "path: '"+vitePath+"'") {
		t.Errorf("vite.config.ts must set server.hmr.path to %q to match devViteHMRPath", vitePath)
	}
	if strings.Contains(config, "clientPort") {
		t.Error("vite.config.ts must not pin hmr.clientPort: the socket reaches vite through the Traefik route, not on a second port")
	}
	if want := "vite dev --port " + strconv.Itoa(devVitePort); !strings.Contains(manifest, want) {
		t.Errorf("devVitePort is %d but the web workspace does not run %q", devVitePort, want)
	}
}

func TestWriteAndRemoveDevViteRoute(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dynamic")
	if err := writeDevViteRoute(dir, devVitePort); err != nil {
		t.Fatalf("write: %v", err)
	}
	written, err := os.ReadFile(filepath.Join(dir, devViteRouteFile))
	if err != nil {
		t.Fatalf("the route file is missing: %v", err)
	}
	if !strings.Contains(string(written), devViteHMRPath) {
		t.Errorf("the route file does not route the HMR path: %s", written)
	}

	if err := removeDevViteRoute(dir); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, devViteRouteFile)); !os.IsNotExist(err) {
		t.Errorf("the route file survived removal: %v", err)
	}
	// A loop that exits twice, or a runtime that never had the file, is fine.
	if err := removeDevViteRoute(dir); err != nil {
		t.Errorf("removing an absent route must not fail: %v", err)
	}
	if err := removeDevViteRoute(""); err != nil {
		t.Errorf("removing with no directory configured must not fail: %v", err)
	}
	if err := writeDevViteRoute("", devVitePort); err == nil {
		t.Error("writing with no directory configured must fail rather than drop the route silently")
	}
}

func readRepoFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
