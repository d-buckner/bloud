// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package appconfig

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func newTestTraefikConfigurator(t *testing.T, httpPort, tlsPort int) *TraefikConfigurator {
	t.Helper()
	return NewTraefikConfigurator(nil, httpPort, tlsPort, 3000, 9001, t.TempDir())
}

func TestStaticConfigHasWebsecureEntrypoint(t *testing.T) {
	c := newTestTraefikConfigurator(t, 8080, 8443)
	cfg := c.staticConfig()

	if !strings.Contains(cfg, `address: ":8080"`) {
		t.Error("missing web entrypoint :8080")
	}
	if !strings.Contains(cfg, `address: ":8443"`) {
		t.Error("missing websecure entrypoint :8443")
	}
	// Traefik v3 has no static tls node: the cert store lives in dynamic
	// config. A static "tls:" block would crash Traefik with
	// "field not found, node: tls".
	if strings.Contains(cfg, "tls:") {
		t.Error("static config must not contain a tls: section (Traefik v3)")
	}
}

func TestTLSDynamicConfigServesBloudLeaf(t *testing.T) {
	c := newTestTraefikConfigurator(t, 8080, 8443)
	cfg := c.tlsDynamicConfig()

	for _, want := range []string{
		"tls:",
		"stores:",
		"defaultCertificate:",
		"certFile: /certs/server.crt",
		"keyFile: /certs/server.key",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("tls dynamic config missing %q:\n%s", want, cfg)
		}
	}
}

func TestDynamicConfigsEmitTLSRouterTwins(t *testing.T) {
	c := newTestTraefikConfigurator(t, 8080, 8443)

	cases := []struct {
		name    string
		cfg     string
		routers []string
	}{
		{"base.yml", c.baseDynamicConfig(), []string{"traefik-dashboard", "host-api", "host-auth", "bloud-ui"}},
		{"authentik-routes.yml", c.authentikRoutes(), []string{"authentik-outpost", "authentik-api", "authentik-application", "authentik-flows", "authentik-if", "authentik-internal", "authentik-static", "authentik-ws"}},
	}
	for _, tc := range cases {
		// Every HTTP router needs a -tls twin bound to websecure.
		for _, base := range tc.routers {
			if !strings.Contains(tc.cfg, base+":") {
				t.Errorf("%s: missing %s router", tc.name, base)
			}
			if !strings.Contains(tc.cfg, base+"-tls:") {
				t.Errorf("%s: missing %s-tls router", tc.name, base)
			}
		}
		if !strings.Contains(tc.cfg, "tls: true") {
			t.Errorf("%s: no tls: true on any router", tc.name)
		}
		// TLS twins must name the websecure entrypoint explicitly; an
		// unnamed router binds to every entrypoint and would conflict
		// with its HTTP twin on websecure.
		if got, want := strings.Count(tc.cfg, "- websecure"), len(tc.routers); got != want {
			t.Errorf("%s: expected websecure entrypoint on %d -tls routers, found %d", tc.name, want, got)
		}
	}
}

func TestPreStartWritesAllFilesAndIsIdempotent(t *testing.T) {
	c := newTestTraefikConfigurator(t, 8080, 8443)
	traefikDir := filepath.Join(c.dataDir, "traefik")

	changed, err := c.PreStart(context.Background(), &configurator.AppState{})
	if err != nil {
		t.Fatalf("first PreStart: %v", err)
	}
	if !changed {
		t.Error("first PreStart should report changed=true")
	}
	for _, p := range []string{
		"traefik.yml",
		"dynamic/base.yml",
		"dynamic/authentik-routes.yml",
		"dynamic/tls.yml",
	} {
		if _, err := os.Stat(filepath.Join(traefikDir, p)); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}

	// Second pass with no drift: must not report a change (otherwise the
	// orchestrator would recreate the container on every reconcile).
	changed, err = c.PreStart(context.Background(), &configurator.AppState{})
	if err != nil {
		t.Fatalf("second PreStart: %v", err)
	}
	if changed {
		t.Error("steady-state PreStart reported changed=true")
	}
}
