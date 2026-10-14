// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"path/filepath"
	"regexp"
	"sort"
)

// devPassthroughEnv names the deployment-wide opt-in development switches the
// CLI forwards from its own environment to the host-agent it starts. The native
// backend would inherit them anyway; the VM backends run the host-agent over
// ssh with an explicit environment, and the e2e runner writes a systemd unit,
// so without this they could never be set there. Every switch listed is off
// unless set.
//
// App-specific switches are NOT listed here: they are discovered from the
// catalog (each app's e2e.env, see appDevEnvKeys), so a new app switch needs no
// CLI edit.
var devPassthroughEnv = []string{
	// Names the upstream TLS terminator Traefik should trust, so a proxied
	// deployment keeps the real scheme instead of rewriting X-Forwarded-Proto
	// to http. Without this the setting cannot reach the host-agent at all on
	// the VM backends or the e2e systemd unit, so a https instance would always
	// derive an http issuer. See docs/plans/archive/upstream-proxy-headers.md.
	"BLOUD_TRUSTED_PROXY_NETS",
	// The deployment-wide public scheme (http|https) for a TLS-terminating
	// proxy. Per-host schemes saved in Settings win over it.
	"BLOUD_PUBLIC_SCHEME",
}

// appDevEnvKeys returns every app-specific dev switch declared across the
// catalog (the keys of each app's e2e.env), sorted so the render order is
// stable.
func appDevEnvKeys() []string {
	root, err := getProjectRoot()
	if err != nil {
		return nil
	}
	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, app := range apps {
		for k := range app.E2E.Env {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// passthroughEnvKeys is every switch the CLI forwards, in a stable order:
// deployment-wide first, then app-specific switches from the catalog.
func passthroughEnvKeys() []string {
	return append(append([]string{}, devPassthroughEnv...), appDevEnvKeys()...)
}

// devEnvValue is what a forwarded value may contain. It is deliberately narrow:
// the values are flags and CIDR lists, and a restricted alphabet means a value
// can be written into a systemd unit or an exported shell line without quoting
// rules. Slashes and commas are allowed because a trusted-proxy setting is a
// comma-separated list of CIDRs (10.0.0.20/32,10.89.0.0/16); neither is a
// shell metacharacter and neither needs escaping in a unit file.
var devEnvValue = regexp.MustCompile(`^[A-Za-z0-9_.,/-]+$`)

// devPassthrough returns the opt-in development variables that are set (and
// safe to forward) in the environment read by getenv. Unset variables and
// values outside devEnvValue are omitted, so the result is empty by default.
func devPassthrough(getenv func(string) string) map[string]string {
	out := map[string]string{}
	for _, key := range passthroughEnvKeys() {
		if v := getenv(key); v != "" && devEnvValue.MatchString(v) {
			out[key] = v
		}
	}
	return out
}
