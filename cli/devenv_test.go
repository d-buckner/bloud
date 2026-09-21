// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"reflect"
	"strings"
	"testing"
)

func envFrom(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDevPassthroughIsEmptyByDefault(t *testing.T) {
	if got := devPassthrough(envFrom(nil)); len(got) != 0 {
		t.Fatalf("devPassthrough with nothing set = %v, want empty", got)
	}
}

func TestDevPassthroughForwardsOnlyKnownSwitches(t *testing.T) {
	got := devPassthrough(envFrom(map[string]string{
		"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP": "1",
		"BLOUD_DEV_SOMETHING_ELSE":         "1", // not a known switch
		"PATH":                             "/usr/bin",
	}))
	want := map[string]string{"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP": "1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devPassthrough = %v, want %v", got, want)
	}
}

func TestDevPassthroughDropsValuesThatNeedQuoting(t *testing.T) {
	// The value ends up in a systemd unit and in an exported shell line, so
	// anything outside a narrow alphabet is dropped rather than escaped.
	for _, v := range []string{"1; rm -rf /", "a b", "$(id)", "x\ny", "`id`", "'quoted'", "a=b"} {
		got := devPassthrough(envFrom(map[string]string{"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP": v}))
		if len(got) != 0 {
			t.Errorf("value %q was forwarded: %v", v, got)
		}
	}
}

func TestLifecycleUnitCarriesTheDevSwitchOnlyWhenSet(t *testing.T) {
	base := lifecycleConfig{remoteDir: "/tmp/bloud-e2e", traefikDir: "/srv/traefik/dynamic"}

	if unit := renderLifecycleHostAgentUnit(base); strings.Contains(unit, "BLOUD_DEV_") {
		t.Fatalf("unit carries a dev switch nobody set:\n%s", unit)
	}

	on := base
	on.devEnv = map[string]string{"BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP": "1"}
	unit := renderLifecycleHostAgentUnit(on)
	if !strings.Contains(unit, "Environment=BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1\n") {
		t.Fatalf("unit missing the dev switch:\n%s", unit)
	}
}

func TestParseLifecycleConfigPicksUpTheDevSwitch(t *testing.T) {
	cfg, _, err := parseLifecycleConfig(t.TempDir(), []string{"--host-only"}, func(k string) string {
		switch k {
		case "BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP":
			return "true"
		case "BLOUD_E2E_RUNTIME_DIR":
			return "/var/tmp/bloud-e2e-runtime"
		}
		return ""
	}, "native")
	if err != nil {
		t.Fatalf("parseLifecycleConfig: %v", err)
	}
	if cfg.devEnv["BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP"] != "true" {
		t.Fatalf("devEnv = %v, want the switch forwarded", cfg.devEnv)
	}
}
