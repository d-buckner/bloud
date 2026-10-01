// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"reflect"
	"testing"
	"time"
)

func TestSplitNets(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"10.0.2.0/24", []string{"10.0.2.0/24"}},
		{"10.0.2.2", []string{"10.0.2.2"}},
		{"10.0.2.0/24, 10.0.3.2", []string{"10.0.2.0/24", "10.0.3.2"}},
		{"bad, 10.0.2.2, not-a-cidr", []string{"10.0.2.2"}},
		{" , 10.0.2.0/24 , ", []string{"10.0.2.0/24"}},
	}
	for _, tc := range cases {
		got := splitNets(tc.raw)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("splitNets(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestGetEnvDuration(t *testing.T) {
	const key = "BLOUD_TEST_DURATION"
	const def = 90 * time.Second

	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", def}, // unset: the caller's default
		{"30s", 30 * time.Second},
		{"5m", 5 * time.Minute},
		{" 45s ", 45 * time.Second}, // surrounding space is operator noise
		{"off", -1},                 // the literal disable spelling
		{"OFF", -1},
		{"none", -1},
		{"disabled", -1},
		{"-1s", -1 * time.Second}, // an explicit non-positive also disables
		{"nonsense", def},         // unparseable: the default, not a crash
	}

	for _, tc := range cases {
		t.Setenv(key, tc.raw)
		if got := getEnvDuration(key, def); got != tc.want {
			t.Errorf("getEnvDuration(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

// The reconcile interval is the self-healing knob: unset means "not
// configured", which the wiring layer resolves to the framework default.
func TestLoadReconcileInterval(t *testing.T) {
	t.Setenv("BLOUD_RECONCILE_INTERVAL", "")
	if got := getEnvDuration("BLOUD_RECONCILE_INTERVAL", 0); got != 0 {
		t.Errorf("unset interval = %v, want 0 (meaning: use the framework default)", got)
	}

	t.Setenv("BLOUD_RECONCILE_INTERVAL", "10m")
	if got := getEnvDuration("BLOUD_RECONCILE_INTERVAL", 0); got != 10*time.Minute {
		t.Errorf("BLOUD_RECONCILE_INTERVAL=10m parsed as %v", got)
	}
}
