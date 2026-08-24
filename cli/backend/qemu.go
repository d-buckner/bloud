// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	// qemu-img resize and QEMU -m accept the short suffixes (G), not GiB.
	qemuCPUs   = 4
	qemuMemory = "4G"
	qemuDisk   = "30G"
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
	instance     string
	projectDir   string
	dir          string // runtime dir: <root>/.bloud/qemu/<instance>
	newCmd       func(ctx context.Context, name string, args ...string) *exec.Cmd
	pollInterval time.Duration // readiness poll cadence (tests set to 0)
	pollTimeout  time.Duration // readiness poll deadline (tests shrink)
	// runGuest executes a shell script on the guest via SSH (used for the
	// front-proxy unit install). Tests override it; nil = real SSH.
	runGuest func(ctx context.Context, script string) error
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
		pollTimeout:  10 * time.Minute,
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
	// Install the front proxy unit (port 80 → Traefik 8080) on existing VMs.
	// Fresh VMs get it from the cloud-init runcmd; here it is a best-effort
	// upgrade (sudo) so a missing unit never blocks `./bloud dev`.
	if err := b.ensureFrontProxyUnit(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not install bloud-front.service (port 80 unavailable until the VM is recreated): %v\n", err)
	}
	// Debian cloud images ship no 9p/virtiofs kernel modules, so a live host
	// mount is unavailable; instead rsync the project into the guest so the
	// dev loop (apps/, services/) sees a working copy.
	if err := b.SyncProject(ctx); err != nil {
		return fmt.Errorf("sync project into guest: %w", err)
	}
	return nil
}

// SyncProject copies the host project dir into the guest (incremental via
// rsync), skipping heavy/irrelevant dirs. The guest path equals the host path.
func (b *QEMUBackend) SyncProject(ctx context.Context) error {
	args := []string{
		"-a", "-e", "ssh -p " + strconv.Itoa(qemuSSHPort) + " -i " + filepath.Join(b.dir, "id_ed25519"),
		"--exclude=.git", "--exclude=.forgejo", "--exclude=.bloud",
		"--exclude=node_modules", "--exclude=build", "--exclude=dist",
		"--exclude=coverage", "--exclude=bloud",
	}
	args = append(args, b.projectDir+"/", qemuSSHUser+"@127.0.0.1:"+b.projectDir+"/")
	_, err := b.run(ctx, "rsync", args...)
	return err
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
			"traefik":    "8080",
			"ldap":       "3389",
			"jellyfin":   "8096",
			"authentik":  "9001",
			"immich":     "2283",
			"navidrome":  "4533",
			"affine":     "3010",
			"appflowy":   "8480",
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
	if err := os.WriteFile(filepath.Join(b.dir, "user-data"), []byte(buildUserData(b.instance, b.projectDir, strings.TrimSpace(string(pub)), os.Geteuid())), 0o644); err != nil {
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
	ready := b.guestReady(ctx)
	alive := b.vmAlive()
	if ready && alive && !b.launchArgsChanged() {
		return nil
	}
	// If a tracked qemu process for this VM is already alive, do not spawn a
	// duplicate (it would collide on the pidfile lock).
	if alive {
		if b.launchArgsChanged() {
			fmt.Fprintf(os.Stderr, "QEMU launch args changed; restarting VM %q to apply them...\n", b.instance)
			if err := b.stop(); err != nil {
				return fmt.Errorf("stop QEMU VM for restart: %w", err)
			}
			if err := b.launch(ctx); err != nil {
				return err
			}
			if err := b.waitReady(ctx); err != nil {
				return fmt.Errorf("QEMU guest %q did not become ready after restart: %w", b.instance, err)
			}
			return nil
		}
		if err := b.waitReady(ctx); err != nil {
			return fmt.Errorf("QEMU guest %q did not become ready: %w", b.instance, err)
		}
		return nil
	}
	// Not alive: (re)launch. A reachable guest without a launch record is
	// externally managed (legacy/manual) — do not spawn a duplicate.
	if ready && !b.hasLaunchRecord() {
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

// hasLaunchRecord reports whether a launch-args file exists, i.e. the VM was
// started by a CLI version that records its QEMU args.
func (b *QEMUBackend) hasLaunchRecord() bool {
	_, err := os.ReadFile(filepath.Join(b.dir, b.instance+".launch-args"))
	return err == nil
}

// launchArgsChanged reports whether the QEMU args recorded at last launch
// differ from the current ones (e.g. the host-forward port list changed). A
// VM started with older args must be restarted to pick them up; VMs without a
// recorded file (created before this check) count as changed.
func (b *QEMUBackend) launchArgsChanged() bool {
	saved, err := os.ReadFile(filepath.Join(b.dir, b.instance+".launch-args"))
	if err != nil {
		return true
	}
	want := strings.Join(b.launchArgs(filepath.Join(b.dir, b.instance+".pid")), "\n")
	return string(saved) != want
}

// stop stops the running VM (SIGTERM to the recorded pid, wait for exit).
func (b *QEMUBackend) stop() error {
	pidFile := filepath.Join(b.dir, b.instance+".pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return fmt.Errorf("read pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse pidfile: %w", err)
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/proc" + strconv.Itoa(pid)); err != nil {
			os.Remove(pidFile)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out stopping QEMU VM (pid %d)", pid)
}

// frontProxyInstallScript installs and enables the port-80 front proxy system
// unit (the proxy itself serves the "starting up" fallback page). It runs as
// root: via the cloud-init runcmd on first boot, or via sudo when upgrading an
// existing VM. The unit's ExecStartPre waits for the host-agent binary that
// `./bloud dev` deploys into the guest.
const frontProxyInstallScript = `set -e
/sbin/sysctl -w net.ipv4.ip_unprivileged_port_start=80
cat > /etc/systemd/system/bloud-front.service << 'UNIT'
[Unit]
Description=Bloud front proxy (port 80 -> Traefik 8080, startup page)
After=network.target
StartLimitIntervalSec=0

[Service]
Type=simple
Environment=BLOUD_FRONT_PORT=80 BLOUD_TRAEFIK_PORT=8080 BLOUD_PORT=3000
ExecStartPre=/sbin/sysctl -w net.ipv4.ip_unprivileged_port_start=80
ExecStartPre=/bin/sh -c 'i=0; while [ $i -lt 120 ]; do [ -x ` + qemuRemoteDir + `/host-agent/host-agent ] && exit 0; sleep 1; i=$((i+1)); done; exit 1'
ExecStart=` + qemuRemoteDir + `/host-agent/host-agent front-proxy
Restart=always
RestartSec=2

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now bloud-front.service
`

// ensureFrontProxyUnit installs/refreshes the front proxy unit on a running VM.
// It is idempotent, so running it on every Create keeps existing VMs in sync.
// The unit is a system unit; the dev user has passwordless sudo (sudoers
// drop-in in the cloud-init spec), so the script runs through sudo -n.
func (b *QEMUBackend) ensureFrontProxyUnit(ctx context.Context) error {
	wrapped := "sudo -n sh -c '" + strings.ReplaceAll(frontProxyInstallScript, "'", "'\\''") + "'"
	if b.runGuest != nil {
		return b.runGuest(ctx, wrapped)
	}
	res, err := b.Host().Executor().Run(ctx, executor.RunSpec{Command: wrapped})
	if err != nil {
		return fmt.Errorf("install front proxy unit: %v: %s", err, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// vmAlive reports whether a qemu process for this VM is currently running,
// using the pidfile written at launch.
func (b *QEMUBackend) vmAlive() bool {
	data, err := os.ReadFile(filepath.Join(b.dir, b.instance+".pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// launchArgs builds the QEMU argument list. pidFile is where the daemonized
// process writes its pid. The args are recorded at launch (see
// launchArgsChanged) so an existing VM is restarted when they change.
func (b *QEMUBackend) launchArgs(pidFile string) []string {
	disk := filepath.Join(b.dir, b.instance+".qcow2")
	seed := filepath.Join(b.dir, "seed.iso")
	// Host-side forward ports. Each guest port can be remapped to a different
	// host port via BLOUD_QEMU_FWD_<guestport> (e.g. BLOUD_QEMU_FWD_9001=9101)
	// so the VM can run on hosts that already occupy a default port. The guest
	// ports themselves never change; only the host port QEMU binds changes.
	fwds := make([]string, 0, 10)
	fwds = append(fwds, fmt.Sprintf("hostfwd=tcp::%s-:22", hostForwardPort(strconv.Itoa(qemuSSHPort))))
	for _, gp := range []string{"80", "3000", "3389", "8080", "8096", "9001", "2283", "4533", "3010", "8480"} {
		hp := hostForwardPort(gp)
		if gp == "80" && !canBindHostPort(hp) {
			// Non-root hosts cannot bind port 80 (no cap_net_bind_service).
			// Fall back to a deterministic high port so the front proxy is
			// still reachable (http://jellyfin.localhost:<port>); true
			// no-port URLs need: sudo setcap 'cap_net_bind_service=+ep'
			// $(command -v qemu-system-x86_64)  (or BLOUD_QEMU_FWD_80).
			chosen := ""
			for _, cand := range []string{"8088", "8089", "8090"} {
				if canBindHostPort(cand) {
					chosen = cand
					break
				}
			}
			if chosen == "" {
				fmt.Fprintf(os.Stderr, "warning: no bindable host port for guest 80; skipping the port-80 forward.\n")
				continue
			}
			fmt.Fprintf(os.Stderr, "note: host cannot bind port 80 (non-root without cap_net_bind_service); forwarding guest 80 to host %s instead (http://jellyfin.localhost:%s). For true no-port URLs run: sudo setcap 'cap_net_bind_service=+ep' $(command -v qemu-system-x86_64)\n", chosen, chosen)
			hp = chosen
		}
		fwds = append(fwds, fmt.Sprintf("hostfwd=tcp::%s-:%s", hp, gp))
	}
	// mDNS: forward unicast UDP 5353 so the guest's .local announcer is
	// reachable from the host (slirp does not relay multicast). The host
	// port follows the usual BLOUD_QEMU_FWD_5353 remap — useful for
	// verification on hosts whose own responder (usually avahi-daemon)
	// owns 5353: dig @127.0.0.1 -p <port>. Standard mDNS clients only
	// speak 5353, so a remap never serves them.
	mdHostPort := hostForwardPort("5353")
	if canBindHostUDPPort(mdHostPort) {
		fwds = append(fwds, fmt.Sprintf("hostfwd=udp::%s-:5353", mdHostPort))
	} else {
		fmt.Fprintf(os.Stderr, "note: host UDP %s is busy (the host's mDNS responder, usually avahi-daemon, owns 5353); mDNS from the guest VM is not reachable from the host. To forward it: sudo systemctl stop avahi-daemon — or verify with BLOUD_QEMU_FWD_5353=<free-port> + dig @127.0.0.1 -p <free-port>\n", mdHostPort)
	}
	netdev := "user,id=net0," + strings.Join(fwds, ",")
	return []string{
		"-machine", "q35,accel=kvm",
		"-cpu", "max",
		"-m", qemuMemory,
		"-smp", strconv.Itoa(qemuCPUs),
		"-drive", "file=" + disk + ",if=virtio",
		"-drive", "file=" + seed + ",media=cdrom,readonly=on",
		"-virtfs", fmt.Sprintf("local,path=%s,mount_tag=host0,id=host0,security_model=passthrough", b.projectDir),
		"-netdev", netdev,
		"-device", "virtio-net-pci,netdev=net0",
		"-display", "none",
		"-daemonize", "-pidfile", pidFile,
	}
}

// launch starts the headless QEMU VM in the background (daemonized) and
// records the launch args for the restart-on-change check.
func (b *QEMUBackend) launch(ctx context.Context) error {
	pid := filepath.Join(b.dir, b.instance+".pid")
	args := b.launchArgs(pid)
	if _, err := b.run(ctx, "qemu-system-x86_64", args...); err != nil {
		return fmt.Errorf("failed to launch QEMU VM: %w", err)
	}
	if err := os.WriteFile(filepath.Join(b.dir, b.instance+".launch-args"),
		[]byte(strings.Join(args, "\n")), 0o644); err != nil {
		return fmt.Errorf("record launch args: %w", err)
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
	// Pass the marker check as separate ssh args so they join into a single
	// remote command (`test -f <marker>`), rather than `bash -c test ...` which
	// would only run the bare `test` builtin and always fail.
	_, err := b.runSSH(ctx, "test", "-f", qemuReadyMark)
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
// the bloud user with our SSH key (uid pinned to the host uid so rsync/podman
// ownership maps cleanly), the podman toolchain, the podman API socket (the
// host-agent's podman client needs /run/user/1000/podman/podman.sock), and a
// ready marker. The project is copied in later via rsync (syncProject), not a
// live mount.
func buildUserData(instance, projectDir, pubKey string, hostUID int) string {
	return fmt.Sprintf(`#cloud-config
disable_root: true
ssh_pwauth: false
users:
  - name: bloud
    uid: %d
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - %s
packages:
  - podman
  - golang-go
  - unzip
  - curl
  - jq
  - rsync
  - ldap-utils
runcmd:
  - mkdir -p %s && chown bloud:bloud %s
  - loginctl enable-linger bloud
  - systemctl --user enable --now podman.socket
  - mkdir -p %s && chown bloud:bloud %s
  - touch %s
  - sh -c 'echo %s | base64 -d | sh'
`, hostUID, pubKey, projectDir, projectDir, qemuRemoteDir, qemuRemoteDir, qemuReadyMark,
		base64.StdEncoding.EncodeToString([]byte(frontProxyInstallScript)))
}

// hostForwardPort returns the host-side port for a guest port, defaulting to the
// guest port itself. BLOUD_QEMU_FWD_<guestport> overrides it for busy hosts.
func hostForwardPort(guestPort string) string {
	if v := os.Getenv("BLOUD_QEMU_FWD_" + guestPort); v != "" {
		return v
	}
	return guestPort
}

// canBindHostPort probes whether the current user can bind the given TCP port
// (privileged ports fail for non-root without cap_net_bind_service). The probe
// listener is closed immediately; QEMU binds for real at launch.
func canBindHostPort(port string) bool {
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

// canBindHostUDPPort probes whether the current user can bind the given UDP
// port (mDNS 5353 is typically held by the host's own mDNS responder). The
// probe socket is closed immediately; QEMU binds for real at launch.
func canBindHostUDPPort(port string) bool {
	pc, err := net.ListenPacket("udp", "0.0.0.0:"+port)
	if err != nil {
		return false
	}
	pc.Close()
	return true
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
