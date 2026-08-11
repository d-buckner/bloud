package backend

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeQEMUBackend builds a QEMUBackend with a faked command factory. sshReadyResults
// scripts the ssh readiness probe outcomes ("true" probes); provisioning marker
// checks ("test -f ...") always succeed.
func fakeQEMUBackend(t *testing.T, recorded *[][]string, sshReadyResults []bool) *QEMUBackend {
	t.Helper()
	var call int
	b := &QEMUBackend{
		instance:     "bloud-qemu",
		projectDir:   "/repo",
		dir:          t.TempDir(),
		pollInterval: 0,
		pollTimeout:  50 * time.Millisecond,
	}
	b.newCmd = func(_ context.Context, name string, args ...string) *exec.Cmd {
		*recorded = append(*recorded, append([]string{name}, args...))
		if name == "ssh" {
			if len(args) > 0 && args[len(args)-1] == "true" {
				idx := call
				if idx >= len(sshReadyResults) {
					idx = len(sshReadyResults) - 1
				}
				call++
				if sshReadyResults[idx] {
					return exec.Command("true")
				}
				return exec.Command("sh", "-c", "exit 1")
			}
			// provisioning marker check → assume provisioned
			return exec.Command("true")
		}
		if name == "ssh-keygen" {
			// Emulate key generation: create <key> and <key>.pub so ReadFile succeeds.
			for i, a := range args {
				if a == "-f" && i+1 < len(args) {
					os.WriteFile(args[i+1], []byte("key"), 0600)
					os.WriteFile(args[i+1]+".pub", []byte("ssh-ed25519 AAAAC3Nza fake@host\n"), 0644)
					break
				}
			}
		}
		if name == "curl" {
			// -o <file>: write the downloaded base image
			for i, a := range args {
				if a == "-o" && i+1 < len(args) {
					os.WriteFile(args[i+1], []byte("base"), 0644)
					break
				}
			}
		}
		if name == "qemu-img" && len(args) > 0 && args[0] == "create" {
			// create overlay disk at the final positional arg
			os.WriteFile(args[len(args)-1], []byte("disk"), 0644)
		}
		if name == "mkisofs" {
			// -output <file>: write the seed ISO
			for i, a := range args {
				if a == "-output" && i+1 < len(args) {
					os.WriteFile(args[i+1], []byte("seed"), 0644)
					break
				}
			}
		}
		return exec.Command("true")
	}
	return b
}

func TestQEMUBackendCreateAlreadyProvisionedAndRunning(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, []bool{true})
	// Preset the disk + seed so provisioning steps are skipped.
	os.WriteFile(filepath.Join(b.dir, b.instance+".qcow2"), []byte("disk"), 0644)
	os.WriteFile(filepath.Join(b.dir, "seed.iso"), []byte("seed"), 0644)

	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	// Only ssh readiness + provisioning checks run; no download/build/launch.
	for _, cmd := range recorded {
		switch cmd[0] {
		case "curl", "qemu-img", "ssh-keygen", "mkisofs", "qemu-system-x86_64":
			t.Fatalf("unexpected command %q for already-running guest", cmd[0])
		}
	}
	if len(recorded) < 2 {
		t.Fatalf("expected ssh checks, got %v", recorded)
	}
}

func TestQEMUBackendCreateProvisionsAndLaunches(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, []bool{false, true})

	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	names := make([]string, len(recorded))
	for i, cmd := range recorded {
		names[i] = cmd[0]
	}
	want := []string{
		"curl", "qemu-img", "qemu-img", "ssh-keygen", "mkisofs",
		"ssh", "qemu-system-x86_64", "ssh", "ssh",
	}
	if !slicesEqual(names, want) {
		t.Fatalf("command sequence = %v, want %v", names, want)
	}

	// Verify the provisioning artifacts were written.
	if _, err := os.Stat(filepath.Join(b.dir, b.instance+".qcow2")); err != nil {
		t.Errorf("disk not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "seed.iso")); err != nil {
		t.Errorf("seed not created: %v", err)
	}
	for _, f := range []string{"user-data", "meta-data", "id_ed25519", "id_ed25519.pub"} {
		if _, err := os.Stat(filepath.Join(b.dir, f)); err != nil {
			t.Errorf("%s not written: %v", f, err)
		}
	}
	userData, err := os.ReadFile(filepath.Join(b.dir, "user-data"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(userData), "name: bloud") {
		t.Errorf("user-data missing bloud user")
	}
	if !strings.Contains(string(userData), "ssh_authorized_keys") {
		t.Errorf("user-data missing ssh_authorized_keys")
	}
	if !strings.Contains(string(userData), "mount -t 9p") || !strings.Contains(string(userData), "host0 ") {
		t.Errorf("user-data missing virtio-9p project mount")
	}
}

func TestQEMUBackendCreateLaunchArgs(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, []bool{false, true})
	if err := b.Create(context.Background()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	var launch []string
	for _, cmd := range recorded {
		if cmd[0] == "qemu-system-x86_64" {
			launch = cmd
		}
	}
	if launch == nil {
		t.Fatal("no qemu-system-x86_64 launch recorded")
	}
	joined := strings.Join(launch, " ")
	for _, wantArg := range []string{"q35,accel=kvm", "-cpu max", "-m 4GiB", "-smp 4",
		"-daemonize", "-pidfile", "hostfwd=tcp::2222-:22", "hostfwd=tcp::3000-:3000",
		"hostfwd=tcp::5432-:5432", "hostfwd=tcp::8080-:8080", "hostfwd=tcp::8096-:8096",
		"hostfwd=tcp::9000-:9000", "seed.iso", "virtio-net-pci", "-virtfs", "mount_tag=host0"} {
		if !strings.Contains(joined, wantArg) {
			t.Errorf("launch args missing %q: %s", wantArg, joined)
		}
	}
}

func TestQEMUBackendCreateTimeout(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, []bool{false})
	b.pollInterval = 5 * time.Millisecond
	b.pollTimeout = 20 * time.Millisecond
	if err := b.Create(context.Background()); err == nil {
		t.Fatal("Create() error = nil, want non-nil")
	}
}

func TestQEMUBackendDestroy(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, nil)
	if err := os.WriteFile(filepath.Join(b.dir, b.instance+".pid"), []byte("4242\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := b.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
	if len(recorded) != 2 || recorded[0][0] != "kill" || recorded[0][1] != "4242" {
		t.Fatalf("invocations = %v, want kill 4242 then rm -rf", recorded)
	}
	if recorded[1][0] != "rm" || recorded[1][1] != "-rf" {
		t.Fatalf("invocations = %v, want rm -rf runtime dir", recorded)
	}
}

func TestQEMUBackendHost(t *testing.T) {
	var recorded [][]string
	b := fakeQEMUBackend(t, &recorded, nil)
	h := b.Host()
	if h.Executor() == nil {
		t.Error("Host().Executor() = nil")
	}
	if got := h.Ports()["traefik"]; got != "8080" {
		t.Errorf("Ports()[traefik] = %q, want 8080", got)
	}
	if got := h.Ports()["host-agent"]; got != "3000" {
		t.Errorf("Ports()[host-agent] = %q, want 3000", got)
	}
	dirs := h.DataDirs()
	if dirs.HostAgentDir != "/var/tmp/bloud-qemu-runtime/host-agent" {
		t.Errorf("HostAgentDir = %q, want /var/tmp/bloud-qemu-runtime/host-agent", dirs.HostAgentDir)
	}
	if dirs.DataDir != "/var/tmp/bloud-qemu-runtime/data" {
		t.Errorf("DataDir = %q, want /var/tmp/bloud-qemu-runtime/data", dirs.DataDir)
	}
	if dirs.AppsDir != "/repo/apps" {
		t.Errorf("AppsDir = %q, want /repo/apps", dirs.AppsDir)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
