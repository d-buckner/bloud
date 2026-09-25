// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	fedoraOSRelease = `NAME="Fedora Linux"
VERSION="42 (Workstation Edition)"
ID=fedora
VERSION_ID=42
PRETTY_NAME="Fedora Linux 42 (Workstation Edition)"
`
	fedoraDerivativeRelease = `NAME="Bazzite"
ID=bazzite
ID_LIKE="fedora"
`
	ubuntuOSRelease = `NAME="Ubuntu"
VERSION="24.04 LTS (Noble Numbat)"
ID=ubuntu
ID_LIKE=debian
`
	archOSRelease = `NAME="Arch Linux"
ID=arch
`
)

// useRelease points the host-release lookup at a fixture for the duration of
// the test, so a test asserts what it means regardless of the machine it runs
// on.
func useRelease(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	prev := osReleasePath
	osReleasePath = path
	t.Cleanup(func() { osReleasePath = prev })
}

func TestLoadRelease(t *testing.T) {
	rel := loadRelease(filepath.Join(t.TempDir(), "missing"))
	if rel.ID != "" || len(rel.IDLike) != 0 {
		t.Fatalf("missing file should yield an empty Release, got %+v", rel)
	}

	rel = loadRelease(writeFixture(t, fedoraOSRelease))
	if rel.ID != "fedora" {
		t.Fatalf("ID = %q, want fedora", rel.ID)
	}

	rel = loadRelease(writeFixture(t, ubuntuOSRelease))
	if rel.ID != "ubuntu" || len(rel.IDLike) != 1 || rel.IDLike[0] != "debian" {
		t.Fatalf("ubuntu release parsed wrong: %+v", rel)
	}
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReleaseIsFedora(t *testing.T) {
	cases := []struct {
		name string
		rel  Release
		want bool
	}{
		{"fedora", Release{ID: "fedora"}, true},
		{"derivative via ID_LIKE", Release{ID: "bazzite", IDLike: []string{"fedora"}}, true},
		{"ubuntu", Release{ID: "ubuntu", IDLike: []string{"debian"}}, false},
		{"arch", Release{ID: "arch"}, false},
		{"unknown host", Release{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rel.IsFedora(); got != tc.want {
				t.Fatalf("IsFedora(%+v) = %v, want %v", tc.rel, got, tc.want)
			}
		})
	}
}

// The native backend runs host-agent directly on the host, with no VM
// boundary. On Fedora it must never be reachable: not offered, and refused
// even when named explicitly in the environment.
func TestNativeIsBlockedOnFedora(t *testing.T) {
	blocked, reason := nativeBlockedOn("linux", Release{ID: "fedora"})
	if !blocked {
		t.Fatal("native must be blocked on Fedora")
	}
	if !strings.Contains(reason, "qemu") {
		t.Fatalf("the refusal should name the backend to use instead: %q", reason)
	}

	if blocked, _ := nativeBlockedOn("linux", Release{ID: "bazzite", IDLike: []string{"fedora"}}); !blocked {
		t.Fatal("a Fedora derivative must be blocked too")
	}
}

func TestNativeIsAllowedElsewhere(t *testing.T) {
	for _, rel := range []Release{{ID: "ubuntu", IDLike: []string{"debian"}}, {ID: "arch"}, {}} {
		if blocked, reason := nativeBlockedOn("linux", rel); blocked {
			t.Fatalf("native should be allowed on %+v, got: %s", rel, reason)
		}
	}
	// macOS never gets native regardless of the release metadata.
	if blocked, _ := nativeBlockedOn("darwin", Release{ID: "fedora"}); blocked {
		t.Fatal("darwin has no native backend to block")
	}
}

func TestCheckBackendRefusesNativeOnFedora(t *testing.T) {
	fedora := Release{ID: "fedora"}
	if err := checkBackend("native", fedora); err == nil {
		t.Fatal("checkBackend must refuse native on Fedora")
	}
	for _, name := range []string{"qemu", "lima"} {
		if err := checkBackend(name, fedora); err != nil {
			t.Fatalf("checkBackend(%q) on Fedora should pass: %v", name, err)
		}
	}
	if err := checkBackend("native", Release{ID: "ubuntu"}); err != nil {
		t.Fatalf("checkBackend(native) on Ubuntu should pass: %v", err)
	}
}

func TestAvailableBackendsForFedoraDropsNative(t *testing.T) {
	if got := strings.Join(availableBackendsFor("linux", Release{ID: "fedora"}), ","); got != "qemu" {
		t.Fatalf("Fedora backends = %q, want qemu only", got)
	}
	if got := strings.Join(availableBackendsFor("linux", Release{ID: "arch"}), ","); got != "qemu,native" {
		t.Fatalf("non-Fedora Linux backends = %q, want qemu,native", got)
	}
}

// A blocked override must not be reachable through any resolution path, and
// must not be advertised in usage text either.
func TestBlockedOverrideIsRefusedEverywhere(t *testing.T) {
	useRelease(t, fedoraOSRelease)

	t.Setenv("BLOUD_BACKEND", "native")

	if _, err := backendName(); err == nil {
		t.Fatal("backendName must refuse BLOUD_BACKEND=native on Fedora")
	} else if !strings.Contains(err.Error(), "qemu") {
		t.Fatalf("refusal should point at qemu: %v", err)
	}

	root := t.TempDir()
	envSet := func(k string) string {
		if k == "BLOUD_BACKEND" {
			return "native"
		}
		return ""
	}
	ask := func(opts []string) (string, error) { return opts[0], nil }
	if _, err := resolveBackend(root, envSet, true, ask); err == nil {
		t.Fatal("resolveBackend must refuse an explicit native override on Fedora")
	}

	if got := usageBackend(); got != "" {
		t.Fatalf("usageBackend must not advertise a blocked backend, got %q", got)
	}
}

// A stored native preference is already discarded on Fedora, because
// storedBackend validates against the available list.
func TestStoredNativePreferenceIsIgnoredOnFedora(t *testing.T) {
	useRelease(t, fedoraOSRelease)
	root := t.TempDir()
	if err := savePreferences(root, Preferences{Backend: "native"}); err != nil {
		t.Fatal(err)
	}
	if got := storedBackend(root); got != "" {
		t.Fatalf("stored native should be discarded on Fedora, got %q", got)
	}
}
