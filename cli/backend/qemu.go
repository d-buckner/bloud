package backend

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

const (
	// qemuArch is the guest architecture for the QEMU VM (x86_64 on Linux via KVM).
	qemuArch = "x86_64"
	// qemuImageURL is the Debian 13 genericcloud amd64 base image.
	qemuImageURL = "https://cloud.debian.org/images/cloud/trixie/latest/debian-13-genericcloud-amd64.qcow2"
	// qemuImageBase is the downloaded base image filename.
	qemuImageBase = "debian-13-genericcloud-amd64.qcow2"
	// qemuCPUs / qemuMemory / qemuDisk mirror the Lima dev VM (4 vCPU / 4GiB / 30GiB).
	qemuCPUs   = 4
	qemuMemory = "4GiB"
	qemuDisk   = "30GiB"
	// qemuSSHUser is the guest user provisioned by cloud-init.
	qemuSSHUser = "bloud"
	// qemuSSHPort is the host port forwarded to guest :22 (hostfwd).
	qemuSSHPort = 2222
	// qemuReadyMark is written by cloud-init once provisioning completes.
	qemuReadyMark = "/var/tmp/bloud-qemu-ready"
	// qemuRemoteDir is where the host-agent and app data live inside the guest.
	qemuRemoteDir = "/var/tmp/bloud-qemu-runtime"
)

// QEMUBackend provisions and manages a QEMU VM via qemu-img and qemu-system-x86_64.
// It is the Linux dev backend; Lima (limactl/vz) remains the macOS backend.
type QEMUBackend struct {
	instance   string
	projectDir string
	dir        string            // runtime dir: <root>/.bloud/qemu/<instance>
	newCmd     func(ctx context.Context, name string, args ...string) *exec.Cmd
	pollInterval time.Duration   // readiness poll cadence (tests set to 0)
	pollTimeout  time.Duration   // readiness poll deadline (tests shrink)
}

// NewQEMUBackend returns a backend that manages the named QEMU VM. projectDir is
// the bloud repo root on the host (the apps dir is referenced into the guest).
func NewQEMUBackend(instance, projectDir string) *QEMUBackend {
	return &QEMUBackend{
		instance:     instance,
		projectDir:   projectDir,
		dir:          filepath.Join(projectDir, ".bloud", "qemu", instance),
		newCmd:       exec.CommandContext,
		pollInterval: 2 * time.Second,
		pollTimeout:  3 * time.Minute,
	}
}

// Create ensures the QEMU VM exists, is provisioned, and is running.
func (b *QEMUBackend) Create(ctx context.Context) error {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return fmt.Errorf("create QEMU runtime dir: %w", err)
	}
	if err := b.ensureDiskImage(ctx); err != nil {
		return err
	}
	if err := b.ensureSeed(ctx); err != nil {
		return err
	}
	if err := b.ensureRunning(ctx); err != nil {
		return err
	}
	return nil
}

// Destroy deletes the QEMU VM and its runtime artifacts.
func (b *QEMUBackend) Destroy(ctx context.Context) error {
	pid := filepath.Join(b.dir, b.instance+".pid")
	if data, err := os.ReadFile(pid); err == nil {
		if pidStr := strings.TrimSpace(string(data)); pidStr != "" {
			_, _ = b.run(ctx, "kill", pidStr)
		}
	}
	if _, err := b.run(ctx, "rm", "-rf", b.dir); err != nil {
		return fmt.Errorf("failed to remove QEMU runtime dir: %w", err)
	}
	return nil
}

// Host returns the runtime host backed by the QEMU guest.
func (b *QEMUBackend) Host() executor.Host {
	return executor.NewSSHHost(
		executor.NewSSHExecutor(executor.SSHConn{
			Host:    "127.0.0.1",
			Port:    qemuSSHPort,
			User:    qemuSSHUser,
			KeyFile: filepath.Join(b.dir, "id_ed25519"),
		}),
		map[string]string{
			"host-agent": "3000",
			"postgres":   "5432",
			"traefik":    "8080",
			"jellyfin":   "8096",
			"authentik":  "9000",
		},
		executor.DataDirs{
			HostAgentDir: qemuRemoteDir + "/host-agent",
			DataDir:      qemuRemoteDir + "/data",
			AppsDir:      filepath.Join(b.projectDir, "apps"),
		},
	)
}

// ensureDiskImage downloads the base image (once) and creates the overlay boot
// disk if it does not already exist.
func (b *QEMUBackend) ensureDiskImage(ctx context.Context) error {
	disk := filepath.Join(b.dir, b.instance+".qcow2")
	if _, err := os.Stat(disk); err == nil {
		return nil
	}
	base := filepath.Join(b.dir, qemuImageBase)
	if _, err := os.Stat(base); err != nil {
		if _, err := b.run(ctx, "curl", "-sSL", "-o", base, qemuImageURL); err != nil {
			return fmt.Errorf("download QEMU base image: %w", err)
		}
	}
	if _, err := b.run(ctx, "qemu-img", "create", "-f", "qcow2", "-F", "qcow2", "-b", base, disk); err != nil {
		return fmt.Errorf("create overlay disk: %w", err)
	}
	if _, err := b.run(ctx, "qemu-img", "resize", disk, qemuDisk); err != nil {
		return fmt.Errorf("resize disk: %w", err)
	}
	return nil
}

// ensureSeed generates an ephemeral SSH key and builds the cloud-init NoCloud
// seed ISO if it does not already exist.
func (b *QEMUBackend) ensureSeed(ctx context.Context) error {
	seed := filepath.Join(b.dir, "seed.iso")
	if _, err := os.Stat(seed); err == nil {
		return nil
	}
	key := filepath.Join(b.dir, "id_ed25519")
	if _, err := os.Stat(key); err != nil {
		if _, err := b.run(ctx, "ssh-keygen", "-t", "ed25519", "-f", key, "-N", "", "-q"); err != nil {
			return fmt.Errorf("generate SSH key: %w", err)
		}
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, "user-data"), []byte(buildUserData(b.instance, strings.TrimSpace(string(pub)))), 0o644); err != nil {
		return fmt.Errorf("write user-data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, "meta-data"), []byte("instance-id: "+b.instance+"\nlocal-hostname: "+b.instance+"\n"), 0o644); err != nil {
		return fmt.Errorf("write meta-data: %w", err)
	}
	if _, err := b.runIn(ctx, b.dir, "mkisofs", "-output", seed, "-volid", "cidata", "-joliet", "-rock", "user-data", "meta-data"); err != nil {
		return fmt.Errorf("build seed ISO: %w", err)
	}
	return nil
}

// ensureRunning launches the VM (if the guest is not already provisioned and
// reachable) and waits for it to become ready.
func (b *QEMUBackend) ensureRunning(ctx context.Context) error {
	if b.guestReady(ctx) {
		return nil
	}
	if err := b.launch(ctx); err != nil {
		return err
	}
	if err := b.waitReady(ctx); err != nil {
		return fmt.Errorf("QEMU guest %q did not become ready: %w", b.instance, err)
	}
	return nil
}

// launch starts the headless QEMU VM in the background (daemonized).
func (b *QEMUBackend) launch(ctx context.Context) error {
	disk := filepath.Join(b.dir, b.instance+".qcow2")
	seed := filepath.Join(b.dir, "seed.iso")
	pid := filepath.Join(b.dir, b.instance+".pid")
	netdev := fmt.Sprintf(
		"user,id=net0,hostfwd=tcp::%d-:22,hostfwd=tcp::3000-:3000,hostfwd=tcp::5432-:5432,hostfwd=tcp::8080-:8080,hostfwd=tcp::8096-:8096,hostfwd=tcp::9000-:9000",
		qemuSSHPort)
	args := []string{
		"-machine", "q35,accel=kvm",
		"-cpu", "max",
		"-m", qemuMemory,
		"-smp", strconv.Itoa(qemuCPUs),
		"-drive", "file=" + disk + ",if=virtio",
		"-drive", "file=" + seed + ",media=cdrom,readonly=on",
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0",
		"-display", "none",
		"-daemonize", "-pidfile", pid,
	}
	if _, err := b.run(ctx, "qemu-system-x86_64", args...); err != nil {
		return fmt.Errorf("failed to launch QEMU VM: %w", err)
	}
	return nil
}

// waitReady polls until the guest is provisioned and reachable or the timeout
// (or caller context) expires.
func (b *QEMUBackend) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(b.pollTimeout)
	for time.Now().Before(deadline) {
		if b.guestReady(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(b.pollInterval):
		}
	}
	return fmt.Errorf("timed out waiting for QEMU guest %q", b.instance)
}

// guestReady reports whether SSH is reachable and cloud-init provisioning has
// finished (packages installed + ready marker written).
func (b *QEMUBackend) guestReady(ctx context.Context) bool {
	if !b.sshReady(ctx) {
		return false
	}
	_, err := b.runSSH(ctx, "bash", "-c", "test -f "+qemuReadyMark)
	return err == nil
}

// sshReady reports whether the guest accepts our SSH key.
func (b *QEMUBackend) sshReady(ctx context.Context) bool {
	args := append(b.sshBase(), "-o", "ConnectTimeout=3", b.sshTarget(), "true")
	_, err := b.run(ctx, "ssh", args...)
	return err == nil
}

// runSSH runs a command in the guest over SSH, returning its output.
func (b *QEMUBackend) runSSH(ctx context.Context, name string, args ...string) (string, error) {
	cmdArgs := append(b.sshBase(), b.sshTarget(), name)
	cmdArgs = append(cmdArgs, args...)
	return b.run(ctx, "ssh", cmdArgs...)
}

func (b *QEMUBackend) sshBase() []string {
	return []string{
		"-p", strconv.Itoa(qemuSSHPort),
		"-i", filepath.Join(b.dir, "id_ed25519"),
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

func (b *QEMUBackend) sshTarget() string { return qemuSSHUser + "@127.0.0.1" }

// buildUserData renders the cloud-init cloud-config that provisions the guest:
// the bloud user with our SSH key, the podman toolchain, and a ready marker.
func buildUserData(instance, pubKey string) string {
	return fmt.Sprintf(`#cloud-config
disable_root: true
ssh_pwauth: false
users:
  - name: bloud
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - %s
packages:
  - podman
  - podman-compose
  - golang-go
  - unzip
  - curl
  - jq
  - ldap-utils
runcmd:
  - loginctl enable-linger bloud
  - mkdir -p %s
  - touch %s
`, pubKey, qemuRemoteDir, qemuReadyMark)
}

func (b *QEMUBackend) run(ctx context.Context, name string, args ...string) (string, error) {
	return b.runIn(ctx, "", name, args...)
}

func (b *QEMUBackend) runIn(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := b.newCmd(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %s: %w", name, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.String(), nil
}
