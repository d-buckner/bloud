// SPDX-License-Identifier: AGPL-3.0-only

package main

import "regexp"

// devPassthroughEnv names the opt-in development switches the CLI forwards from
// its own environment to the host-agent it starts. The native backend would
// inherit them anyway; the VM backends run the host-agent over ssh with an
// explicit environment, and the e2e runner writes a systemd unit, so without
// this they could never be set there. Every switch listed is off unless set, and
// each one names the app it affects, so a stray variable cannot change anything
// else.
var devPassthroughEnv = []string{
	// Serves the Vaultwarden web vault over plain HTTP (localhost names only):
	// see apps/vaultwarden/INTEGRATION.md, "Plain HTTP".
	"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP",
	// Names the upstream TLS terminator Traefik should trust, so a proxied
	// deployment keeps the real scheme instead of rewriting X-Forwarded-Proto
	// to http. Without this the setting cannot reach the host-agent at all on
	// the VM backends or the e2e systemd unit, so a https instance would always
	// derive an http issuer. See docs/plans/upstream-proxy-headers.md.
	"BLOUD_TRUSTED_PROXY_NETS",
	// The deployment-wide public scheme (http|https) for a TLS-terminating
	// proxy. Per-host schemes saved in Settings win over it.
	"BLOUD_PUBLIC_SCHEME",
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
	for _, key := range devPassthroughEnv {
		if v := getenv(key); v != "" && devEnvValue.MatchString(v) {
			out[key] = v
		}
	}
	return out
}
