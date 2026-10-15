// SPDX-License-Identifier: AGPL-3.0-only

package portspec

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeApps builds a throwaway catalog dir from a name to metadata.yaml map.
func writeApps(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		appDir := filepath.Join(dir, name)
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", appDir, err)
		}
		if err := os.WriteFile(filepath.Join(appDir, "metadata.yaml"), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// guestPorts flattens the forwards to "guest->host" strings for comparison.
func guestPorts(l List) []string {
	out := make([]string, 0, len(l.Forwards))
	for _, f := range l.Forwards {
		out = append(out, strconv.Itoa(f.Guest)+"->"+strconv.Itoa(f.Host))
	}
	return out
}

func TestDeriveInfraComesFirst(t *testing.T) {
	appsDir := writeApps(t, map[string]string{"jellyfin": "name: jellyfin\nport: 8096\n"})

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	got := guestPorts(list)
	want := []string{"3000->3000", "80->8080", "8096->8096"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("forwards = %v, want %v", got, want)
	}
}

// The QEMU guest has no inbound path but the slirp forward list, so SSH has to
// be in it. Lima forwards SSH on its own and must not get a second one.
func TestDeriveSSHOnlyForQEMU(t *testing.T) {
	appsDir := writeApps(t, map[string]string{"jellyfin": "name: jellyfin\nport: 8096\n"})

	qemu, err := Derive(appsDir, KindQEMU)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if qemu.Forwards[0].Guest != 22 || qemu.Forwards[0].Host != 2222 {
		t.Errorf("QEMU first forward = %d->%d, want 22->2222", qemu.Forwards[0].Guest, qemu.Forwards[0].Host)
	}

	lima, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for _, f := range lima.Forwards {
		if f.Guest == 22 {
			t.Error("Lima list forwards SSH; Lima does that itself")
		}
	}
}

// An app's own port and every port its containers publish are both debug
// surfaces. The container port that repeats the app port is not listed twice.
func TestDeriveIncludesContainerPortsOnce(t *testing.T) {
	appsDir := writeApps(t, map[string]string{
		"immich": `name: immich
displayName: Immich
port: 2283
containers:
  - name: apps-immich-ml
    ports:
      - host: 2283
        container: 2283
      - host: 32283
        container: 5001
`,
	})

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	seen := map[int]int{}
	for _, f := range list.Forwards {
		seen[f.Guest]++
	}
	if seen[2283] != 1 {
		t.Errorf("guest 2283 forwarded %d times, want once", seen[2283])
	}
	if seen[32283] != 1 {
		t.Errorf("guest 32283 forwarded %d times, want once", seen[32283])
	}
	for _, f := range list.Forwards {
		if f.Guest == 32283 && f.Label != "Immich (ml)" {
			t.Errorf("label for the ML container = %q, want %q", f.Label, "Immich (ml)")
		}
	}
}

// Two apps cannot publish the same host port: QEMU rejects the launch and Lima
// fails the forward. The loser is reported, not dropped in silence, because a
// port that quietly disappears is the failure this package exists to end.
func TestDeriveReportsHostPortCollision(t *testing.T) {
	appsDir := writeApps(t, map[string]string{
		"aaa": "name: aaa\ndisplayName: Aaa\nport: 7000\n",
		"bbb": "name: bbb\ndisplayName: Bbb\nport: 7000\n",
	})

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(list.Skipped) != 1 {
		t.Fatalf("skipped = %v, want exactly one", list.Skipped)
	}
	s := list.Skipped[0]
	if s.Guest != 7000 || s.App != "bbb" {
		t.Errorf("skipped = %+v, want guest 7000 app bbb", s)
	}
	if !strings.Contains(s.Reason, "7000") {
		t.Errorf("reason %q does not name the port", s.Reason)
	}
}

// Traefik declares guest 8080 for its own dashboard, and the public
// entrypoint (guest 80) is already published on host 8080. The declared port
// has to lose that argument visibly.
func TestDeriveTraefikDashboardYieldsToTheEntrypoint(t *testing.T) {
	appsDir := writeApps(t, map[string]string{
		"traefik": "name: traefik\ndisplayName: Traefik\nport: 8080\nisSystem: true\n",
	})

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(list.Skipped) != 1 || list.Skipped[0].Guest != 8080 {
		t.Fatalf("skipped = %v, want guest 8080", list.Skipped)
	}
	for _, f := range list.Forwards {
		if f.Guest == 8080 {
			t.Error("guest 8080 is forwarded twice over: once directly, once as the entrypoint")
		}
	}
}

func TestDeriveIgnoresDirectoriesWithoutMetadata(t *testing.T) {
	appsDir := writeApps(t, map[string]string{"jellyfin": "name: jellyfin\nport: 8096\n"})
	if err := os.MkdirAll(filepath.Join(appsDir, "notanapp"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(list.Forwards) != 3 {
		t.Errorf("forwards = %d, want 3 (two infra + jellyfin)", len(list.Forwards))
	}
}

func TestDeriveErrorsWhenTheCatalogCannotBeRead(t *testing.T) {
	if _, err := Derive(filepath.Join(t.TempDir(), "absent"), KindLima); err == nil {
		t.Error("Derive() error = nil for a missing apps dir, want non-nil")
	}
}

func TestDeriveErrorsOnMalformedMetadata(t *testing.T) {
	appsDir := writeApps(t, map[string]string{"broken": "name: [oops\n"})
	if _, err := Derive(appsDir, KindLima); err == nil {
		t.Error("Derive() error = nil for unparseable metadata, want non-nil")
	}
}

// The name in metadata.yaml is what the catalog keys on; the directory name is
// a convention. A port row without a name field still has to say which app it
// belongs to.
func TestDeriveFallsBackToTheDirectoryName(t *testing.T) {
	appsDir := writeApps(t, map[string]string{"jellyfin": "port: 8096\n"})

	list, err := Derive(appsDir, KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	last := list.Forwards[len(list.Forwards)-1]
	if last.App != "jellyfin" {
		t.Errorf("App = %q, want the directory name", last.App)
	}
}

// TestDeriveRealCatalog pins what the shipped catalog produces. A metadata
// change that moves a port shows up here, and the generated documents and this
// list move in the same change.
func TestDeriveRealCatalog(t *testing.T) {
	list, err := Derive(filepath.Join("..", "..", "apps"), KindLima)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}

	got := make([]string, 0, len(list.Forwards))
	for _, f := range list.Forwards {
		got = append(got, portLabel(f))
	}
	want := []string{
		"3000 host-agent API",
		"80 Traefik: the user-facing port",
		"2283 Immich",
		"3010 AFFiNE",
		"3389 Authentik (ldap)",
		"4533 Navidrome",
		"4534 Navidrome MCP",
		"5001 Podly",
		"5055 Seerr",
		"5232 Radicale",
		"6060 Arr MCP",
		"6636 Authentik (ldap)",
		"6881 qBittorrent",
		"7878 Radarr",
		"8000 Paperless-ngx",
		"8081 qBittorrent",
		"8086 Home Assistant MCP",
		"8096 Jellyfin",
		"8123 Home Assistant",
		"8180 Calino",
		"8222 Vaultwarden",
		"8787 Hermes Web UI",
		"8989 Sonarr",
		"9001 Authentik",
		"9002 Penpot",
		"9119 Hermes",
		"9222 AFFiNE MCP",
		"9333 DAV MCP",
		"9696 Prowlarr",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("derived list changed:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The two ports the catalog declares that get no forward, each with its
	// reason. Both are collisions on the host side, and both were silent
	// before this list was derived.
	if len(list.Skipped) != 2 {
		t.Fatalf("skipped = %v, want two entries", list.Skipped)
	}
}

func portLabel(f One) string {
	return strconv.Itoa(f.Guest) + " " + f.Label
}
