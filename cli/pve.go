package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const pveSyncDir = "/tmp/bloud-src"

const (
	pveDefaultVMID    = "9999"
	pveDefaultMemory  = 8192
	pveDefaultCores   = 2
	pveVMName         = "bloud"
	pveISOStorage     = "/var/lib/vz/template/iso"
	pveISOFilename    = "bloud-test.iso"
	pveVMSSHUser      = "bloud"
	pveVMSSHPass      = "bloud"
	pveBootTimeout    = 180
	pveServiceTimeout = 300

	// Installer ISO — live system runs bloud-installer on port 3001 as root (empty password)
	pveInstallerPort    = "3001"
	pveInstallerAPI     = "http://localhost:" + pveInstallerPort + "/api"
	pveInstallTimeout   = 600

	// Disk provisioned for the test VM so the installer has a target
	pveDiskStorage = "local-lvm"
	pveDiskSizeGB  = 40

)

type pveConfig struct {
	Host   string
	VMID   string
	Memory int
	Cores  int
}

func isPVEMode() bool {
	return os.Getenv("BLOUD_PVE_HOST") != ""
}

func getPVEConfig() pveConfig {
	vmid := os.Getenv("BLOUD_PVE_VMID")
	if vmid == "" {
		vmid = pveDefaultVMID
	}
	return pveConfig{
		Host:   os.Getenv("BLOUD_PVE_HOST"),
		VMID:   vmid,
		Memory: pveDefaultMemory,
		Cores:  pveDefaultCores,
	}
}

// ── SSH helpers ────────────────────────────────────────────────────────────────

func pveExec(cfg pveConfig, cmd string) (string, error) {
	c := exec.Command("ssh",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		cfg.Host,
		cmd,
	)
	output, err := c.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func pveExecStream(cfg pveConfig, cmd string) error {
	c := exec.Command("ssh",
		"-o", "ConnectTimeout=10",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		cfg.Host,
		cmd,
	)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func vmExec(ip, cmd string) (string, error) {
	c := exec.Command("sshpass", "-p", pveVMSSHPass,
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		pveVMSSHUser+"@"+ip,
		cmd,
	)
	output, err := c.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func vmExecStream(ip, cmd string) error {
	c := exec.Command("sshpass", "-p", pveVMSSHPass,
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		pveVMSSHUser+"@"+ip,
		cmd,
	)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func vmInteractive(ip, cmd string) error {
	args := []string{
		"-p", pveVMSSHPass,
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-t",
		pveVMSSHUser + "@" + ip,
	}
	if cmd != "" {
		args = append(args, cmd)
	}
	c := exec.Command("sshpass", args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// ── ISO phase helpers ──────────────────────────────────────────────────────────
// The live installer ISO runs as root with an empty password (no bloud user).

func isoExecStream(ip, cmd string) error {
	c := exec.Command("sshpass", "-p", "",
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		"root@"+ip,
		cmd,
	)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func isoExec(ip, cmd string) (string, error) {
	c := exec.Command("sshpass", "-p", "",
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=5",
		"-o", "LogLevel=ERROR",
		"root@"+ip,
		cmd,
	)
	output, err := c.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// waitForISOReady waits for the live installer ISO to boot and accept SSH as root.
// Returns the VM IP, or empty string on timeout.
func waitForISOReady(cfg pveConfig) string {
	log(fmt.Sprintf("Waiting for ISO to boot (timeout: %ds)...", pveBootTimeout))
	for i := 0; i < pveBootTimeout; i++ {
		ip := getVMIP(cfg)
		if ip != "" {
			c := exec.Command("sshpass", "-p", "",
				"ssh",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=3",
				"-o", "LogLevel=ERROR",
				"root@"+ip,
				"true",
			)
			if c.Run() == nil {
				log(fmt.Sprintf("ISO is up at %s", ip))
				return ip
			}
		}
		if i > 0 && i%15 == 0 {
			fmt.Printf("  ... waiting (%d/%ds)\n", i, pveBootTimeout)
		}
		time.Sleep(1 * time.Second)
	}
	return ""
}

// runInstaller drives the installer API: auto-selects disk, triggers install,
// polls until complete, updates Proxmox boot order to disk, then reboots.
func runInstaller(cfg pveConfig, ip string) int {
	log("Getting disk info from installer...")
	disk, err := isoExec(ip, "curl -sf "+pveInstallerAPI+"/disks | jq -r '.autoSelected'")
	if err != nil || disk == "" || disk == "null" {
		errorf("Failed to get auto-selected disk (got %q): %v", disk, err)
		return 1
	}
	log(fmt.Sprintf("Installing to disk: %s", disk))

	log("Starting installation...")
	installBody := fmt.Sprintf(`{"disk":"%s","encryption":false}`, disk)
	out, err := isoExec(ip, fmt.Sprintf(
		`curl -sf -X POST -H 'Content-Type: application/json' -d '%s' `+pveInstallerAPI+`/install`,
		installBody,
	))
	if err != nil {
		errorf("Failed to start installation: %v\n%s", err, out)
		return 1
	}

	log(fmt.Sprintf("Streaming installation progress (timeout: %ds)...", pveInstallTimeout))

	// Stream SSE events from /api/progress in the background.
	// The server closes the stream on complete or failed, so curl exits naturally.
	go func() {
		_ = isoExecStream(ip, fmt.Sprintf(
			`curl -N -sf %s/progress | while IFS= read -r line; do`+
				`  if [[ "$line" == data:* ]]; then`+
				`    json="${line#data: }";`+
				`    phase=$(printf '%%s' "$json" | jq -r '.phase // empty' 2>/dev/null);`+
				`    msg=$(printf '%%s' "$json" | jq -r '.message // empty' 2>/dev/null);`+
				`    [ -n "$msg" ] && echo "  [$phase] $msg";`+
				`  fi;`+
				`done`,
			pveInstallerAPI,
		))
	}()

	var lastPhase string
	for i := 0; i < pveInstallTimeout; i += 2 {
		phase, _ := isoExec(ip, "curl -sf "+pveInstallerAPI+"/status | jq -r '.phase'")
		if phase != lastPhase && phase != "" && phase != "null" {
			lastPhase = phase
		}
		switch phase {
		case "complete":
			log("Installation complete")
			goto installDone
		case "failed":
			// Fetch the last message for diagnostics
			msg, _ := isoExec(ip, "curl -sf "+pveInstallerAPI+"/status | jq -r '.lastMessage // empty'")
			if msg != "" && msg != "null" {
				fmt.Printf("  Last error: %s\n", msg)
			}
			errorf("Installation failed")
			return 1
		}
		if i > 0 && i%60 == 0 {
			fmt.Printf("  ... still installing (%d/%ds)\n", i, pveInstallTimeout)
		}
		time.Sleep(2 * time.Second)
	}
	errorf("Timeout waiting for installation to complete (%ds)", pveInstallTimeout)
	return 1

installDone:
	// Eject the ISO so the disk wins on next boot.
	log("Ejecting ISO...")
	if _, err := pveExec(cfg, fmt.Sprintf("qm set %s --ide2 none,media=cdrom", cfg.VMID)); err != nil {
		warn(fmt.Sprintf("Failed to eject ISO (non-fatal): %v", err))
	}

	// Reboot into the installed system.
	log("Rebooting into installed system...")
	if _, err := pveExec(cfg, fmt.Sprintf("qm reboot %s", cfg.VMID)); err != nil {
		warn(fmt.Sprintf("Failed to reboot VM (non-fatal): %v", err))
	}

	// Wait for the VM to go offline before returning so waitForPVEVMReady
	// doesn't pick up the ISO's still-active QEMU guest agent IP and burn
	// its retry budget on a system that's mid-shutdown.
	for i := 0; i < 30; i++ {
		if getVMIP(cfg) == "" {
			break
		}
		time.Sleep(1 * time.Second)
	}
	return 0
}

// vmMAC returns a stable locally-administered MAC address derived from the VMID.
// Using the same MAC across destroy/recreate cycles means DHCP renews the same
// lease rather than allocating a new IP each time.
// Format: 02:42:00:00:HH:LL where HHLL = VMID as big-endian uint16.
func vmMAC(vmid string) string {
	n := uint64(0)
	for _, c := range vmid {
		if c >= '0' && c <= '9' {
			n = n*10 + uint64(c-'0')
		}
	}
	n &= 0xffff
	return fmt.Sprintf("02:42:00:00:%02x:%02x", (n>>8)&0xff, n&0xff)
}

// ── VM state helpers ───────────────────────────────────────────────────────────

func getVMIP(cfg pveConfig) string {
	cmd := fmt.Sprintf(
		`qm guest cmd %s network-get-interfaces 2>/dev/null | jq -r '.[]["ip-addresses"][]? | select(.["ip-address-type"] == "ipv4") | .["ip-address"]' | grep -v '^127\.' | head -1`,
		cfg.VMID,
	)
	ip, _ := pveExec(cfg, cmd)
	return ip
}

func pveVMIsRunning(cfg pveConfig) bool {
	out, err := pveExec(cfg, fmt.Sprintf("qm status %s 2>/dev/null", cfg.VMID))
	return err == nil && strings.Contains(out, "running")
}

func pveVMExists(cfg pveConfig) bool {
	_, err := pveExec(cfg, fmt.Sprintf("qm status %s 2>/dev/null", cfg.VMID))
	return err == nil
}

// waitForPVEVMReady waits for the VM to get an IP and accept SSH connections.
// Returns the VM IP, or empty string on timeout.
func waitForPVEVMReady(cfg pveConfig) string {
	log(fmt.Sprintf("Waiting for VM to boot (timeout: %ds)...", pveBootTimeout))
	for i := 0; i < pveBootTimeout; i++ {
		ip := getVMIP(cfg)
		if ip != "" {
			c := exec.Command("sshpass", "-p", pveVMSSHPass,
				"ssh",
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
				"-o", "ConnectTimeout=3",
				"-o", "LogLevel=ERROR",
				pveVMSSHUser+"@"+ip,
				"true",
			)
			if c.Run() == nil {
				log(fmt.Sprintf("VM is up at %s", ip))
				return ip
			}
		}
		if i > 0 && i%15 == 0 {
			fmt.Printf("  ... waiting (%d/%ds)\n", i, pveBootTimeout)
		}
		time.Sleep(1 * time.Second)
	}
	return ""
}

// pveDestroyVM stops and destroys the test VM
func pveDestroyVM(cfg pveConfig) {
	log(fmt.Sprintf("Tearing down VM %s...", cfg.VMID))
	_, _ = pveExec(cfg, fmt.Sprintf(
		"qm stop %s 2>/dev/null || true; sleep 3; qm destroy %s --purge 2>/dev/null || true",
		cfg.VMID, cfg.VMID,
	))
	log("VM destroyed")
}

// pveCleanOldVMs removes any existing VMs with the bloud name or target VMID
func pveCleanOldVMs(cfg pveConfig) {
	log("Checking for old test VMs...")
	oldVMIDs, _ := pveExec(cfg, fmt.Sprintf(`qm list 2>/dev/null | awk '$2 == "%s" {print $1}'`, pveVMName))
	for _, id := range strings.Fields(oldVMIDs) {
		warn(fmt.Sprintf("Destroying old VM %s (%s)...", id, pveVMName))
		_, _ = pveExec(cfg, fmt.Sprintf(
			"qm stop %s 2>/dev/null || true; sleep 3; qm destroy %s --purge 2>/dev/null || true",
			id, id,
		))
	}
	if pveVMExists(cfg) {
		warn(fmt.Sprintf("VM %s already exists, destroying...", cfg.VMID))
		pveDestroyVM(cfg)
	}
}

// ── Health checks ──────────────────────────────────────────────────────────────

type pveCheck struct {
	name string
	cmd  string
}

var pveChecks = []pveCheck{
	{"bloud-pull-images completed", `systemctl --user show bloud-pull-images.service -p ActiveState --value | grep -qE 'active|inactive'`},
	{"bloud-apps target is active", `systemctl --user is-active bloud-apps.target`},
	{"host-agent service is active", `systemctl is-active bloud-host-agent.service`},
	{"host-agent API responds", `curl -sf http://localhost:3000/api/health`},
	{"traefik routes to host-agent", `curl -sf http://localhost:8080/api/health`},
	{"web UI is served", `curl -sf http://localhost:8080/ | grep -q html`},
	{"podman containers are running", `podman ps --format '{{.Names}}' | grep -q apps`},
	{"mDNS is active", `systemctl is-active avahi-daemon.service`},
}

func runPVEChecks(ip string) (passed, failed int) {
	fmt.Println()
	log("Running health checks...")
	fmt.Println()
	for _, c := range pveChecks {
		fmt.Printf("  Checking %s... ", c.name)
		if _, err := vmExec(ip, c.cmd); err == nil {
			fmt.Printf("%sPASS%s\n", colorGreen, colorReset)
			passed++
		} else {
			fmt.Printf("%sFAIL%s\n", colorRed, colorReset)
			failed++
		}
	}
	return
}

func printPVEResults(ip string, passed, failed int) {
	fmt.Println()
	fmt.Println("════════════════════════════════════════════════════════════")
	if failed == 0 {
		fmt.Printf("  %sAll %d checks passed%s\n", colorGreen, passed, colorReset)
	} else {
		fmt.Printf("  %s%d passed%s, %s%d failed%s\n",
			colorGreen, passed, colorReset,
			colorRed, failed, colorReset,
		)
	}
	fmt.Printf("  VM IP: %s\n", ip)
	fmt.Println("════════════════════════════════════════════════════════════")
}

// ── ISO deploy ─────────────────────────────────────────────────────────────────

func doDeploy(cfg pveConfig, isoSource string) int {
	if isoSource == "" {
		log("Finding latest GitHub release...")
		out, err := exec.Command("gh", "release", "view", "--json", "assets",
			"--jq", `[.assets[] | select(.name | endswith(".iso"))] | last | .url`,
		).Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			errorf("No ISO source provided and no GitHub release found")
			return 1
		}
		isoSource = strings.TrimSpace(string(out))
		log(fmt.Sprintf("Using latest release: %s", isoSource))
	}

	if strings.HasPrefix(isoSource, "http") {
		log("Downloading ISO to Proxmox...")
		if err := pveExecStream(cfg, fmt.Sprintf("curl -L -o '%s/%s' '%s'", pveISOStorage, pveISOFilename, isoSource)); err != nil {
			errorf("Failed to download ISO: %v", err)
			return 1
		}
	} else {
		log("Copying ISO to Proxmox...")
		c := exec.Command("scp", isoSource, cfg.Host+":"+pveISOStorage+"/"+pveISOFilename)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			errorf("Failed to copy ISO: %v", err)
			return 1
		}
	}

	return 0
}

// ── Builder host helpers ────────────────────────────────────────────────────

// getBuilderHost returns the BLOUD_BUILDER_HOST env var (e.g. "builder@192.168.0.105").
// Empty string means no builder host is configured.
func getBuilderHost() string {
	return os.Getenv("BLOUD_BUILDER_HOST")
}

// builderSSHExec runs a command on the builder host using the default SSH key.
func builderSSHExec(host, cmd string) (string, error) {
	c := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		host,
		cmd,
	)
	out, err := c.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// builderSSHExecStream runs a command on the builder host, streaming output.
func builderSSHExecStream(host, cmd string) error {
	c := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=10",
		"-o", "LogLevel=ERROR",
		host,
		cmd,
	)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// doBuild rsyncs source to the builder host, builds the ISO, then copies it
// to Proxmox ISO storage — replacing the normal ISO download step.
// Requires BLOUD_BUILDER_HOST to be set (e.g. "builder@192.168.0.105").
func doBuild(cfg pveConfig) int {
	host := getBuilderHost()
	if host == "" {
		errorf("BLOUD_BUILDER_HOST is not set. Add it to .env (e.g. builder@192.168.0.105)")
		return 1
	}

	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	log(fmt.Sprintf("Syncing source to builder (%s)...", host))
	rsync := exec.Command("rsync", "-av", "--delete",
		"--exclude=.git/",
		"--exclude=.claude/",
		"--exclude=.turbo/",
		"--exclude=.playwright-mcp/",
		"--exclude=.DS_Store",
		"--exclude=build/",
		"--exclude=node_modules/",
		"--exclude=.direnv/",
		"--exclude=bloud",
		"-e", "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR",
		root+"/",
		host+":~/bloud/",
	)
	rsync.Stdout = os.Stdout
	rsync.Stderr = os.Stderr
	if err := rsync.Run(); err != nil {
		errorf("Failed to sync source: %v", err)
		return 1
	}

	log("Building ISO (first build may take 15-30 minutes)...")
	buildScript := `set -e
export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"
cd ~/bloud
mkdir -p build

echo '==> Building host-agent binary...'
cd services/host-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../build/host-agent ./cmd/host-agent
cd ../..

echo '==> Building installer binary...'
cd services/installer
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../build/installer ./cmd/installer
cd ../..

echo '==> Building frontend...'
npm ci --prefer-offline
npm run build --workspace=services/host-agent/web
cp -r services/host-agent/web/build build/frontend

echo '==> Building installer web...'
npm run build --workspace=@bloud/installer-web
cp -r services/installer/web/build build/installer-web

echo '==> Staging artifacts for Nix...'
git init -q 2>/dev/null || true
git add -A
git add -f build/

echo '==> Building ISO...'
nix build .#packages.x86_64-linux.iso --no-link
echo 'Build complete.'`

	if err := builderSSHExecStream(host, buildScript); err != nil {
		errorf("ISO build failed: %v", err)
		return 1
	}

	// Get the store path (instant — build already done above).
	// Redirect stderr to suppress "dirty tree" warnings; grep ensures we only
	// capture the /nix/store path even if warnings slip through.
	storePath, err := builderSSHExec(host,
		`export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"; `+
			`cd ~/bloud && nix build .#packages.x86_64-linux.iso --no-link --print-out-paths 2>/dev/null | grep '^/nix/store'`,
	)
	if err != nil || storePath == "" {
		errorf("Failed to get ISO store path: %v", err)
		return 1
	}

	isoPath, err := builderSSHExec(host, fmt.Sprintf("find '%s/iso' -name '*.iso' | head -1", storePath))
	if err != nil || isoPath == "" {
		errorf("Could not find .iso file in %s/iso", storePath)
		return 1
	}
	log(fmt.Sprintf("ISO built: %s", isoPath))

	// Copy ISO: builder → local → Proxmox
	localISO := "/tmp/bloud-built.iso"
	log("Downloading ISO from builder...")
	scpDown := exec.Command("scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		host+":"+isoPath,
		localISO,
	)
	scpDown.Stdout = os.Stdout
	scpDown.Stderr = os.Stderr
	if err := scpDown.Run(); err != nil {
		errorf("Failed to download ISO from builder: %v", err)
		return 1
	}
	defer os.Remove(localISO)

	log("Uploading ISO to Proxmox...")
	scpUp := exec.Command("scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		localISO,
		cfg.Host+":"+pveISOStorage+"/"+pveISOFilename,
	)
	scpUp.Stdout = os.Stdout
	scpUp.Stderr = os.Stderr
	if err := scpUp.Run(); err != nil {
		errorf("Failed to upload ISO to Proxmox: %v", err)
		return 1
	}

	log("ISO ready in Proxmox")
	return 0
}

// cmdSetupBuilderPVE provisions the builder host specified by BLOUD_BUILDER_HOST.
// Requires Nix to already be installed on the host.
// Idempotent: skips if ~/.bloud-provisioned exists.
func cmdSetupBuilderPVE() int {
	host := getBuilderHost()
	if host == "" {
		errorf("BLOUD_BUILDER_HOST is not set. Add it to .env (e.g. builder@192.168.0.105)")
		return 1
	}

	log(fmt.Sprintf("Checking builder host (%s)...", host))

	// Verify SSH connectivity
	if _, err := builderSSHExec(host, "echo ok"); err != nil {
		errorf("Cannot reach builder host %s via SSH: %v", host, err)
		return 1
	}

	// Verify Nix is installed
	if _, err := builderSSHExec(host, "command -v nix || /nix/var/nix/profiles/default/bin/nix --version"); err != nil {
		errorf("Nix is not installed on %s. Install it first: https://nixos.org/download", host)
		return 1
	}

	// Check sentinel
	if out, _ := builderSSHExec(host, "test -f ~/.bloud-provisioned && echo yes"); out == "yes" {
		log("Builder already provisioned")
		fmt.Printf("  Run './bloud start --build' to build and test the ISO\n")
		return 0
	}

	log("Provisioning builder with Go and Node.js via Nix...")
	provisionScript := `set -e
export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"

echo '==> Installing git and rsync...'
if ! command -v git >/dev/null || ! command -v rsync >/dev/null; then
    sudo apt-get install -y git rsync
fi

echo '==> Installing Go and Node.js via nix profile...'
nix profile install nixpkgs#go nixpkgs#nodejs_22

echo '==> Verifying...'
go version
node --version

git config --global --add safe.directory '*'
touch ~/.bloud-provisioned
echo 'Provisioning complete.'`

	if err := builderSSHExecStream(host, provisionScript); err != nil {
		errorf("Failed to provision builder: %v", err)
		return 1
	}

	log("Builder provisioned successfully")
	fmt.Printf("  Run './bloud start --build' to build and test the ISO\n")
	return 0
}

// ── Commands ───────────────────────────────────────────────────────────────────

// cmdStartPVE is the main ISO test lifecycle:
// deploy ISO → clean old VMs → create VM → boot live ISO → (optional: install + checks)
// Without --install: boots the live ISO and exits, leaving it running for manual install.
// With --install: drives the installer API automatically, then runs health checks.
// VM stays running after completion. Flags: --skip-deploy (reuse existing VM)
func cmdStartPVE(args []string) int {
	cfg := getPVEConfig()
	build := false
	skipDeploy := false
	autoInstall := false
	isoSource := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--build":
			build = true
		case "--skip-deploy":
			skipDeploy = true
		case "--install":
			autoInstall = true
		case "--pve-host":
			if i+1 < len(args) {
				cfg.Host = args[i+1]
				i++
			}
		case "--vmid":
			if i+1 < len(args) {
				cfg.VMID = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				isoSource = args[i]
			}
		}
	}

	printVMInfo := func() {
		fmt.Printf("  VM is running. To tear down: ./bloud destroy\n")
	}

	var vmIP string

	if !skipDeploy {
		if build {
			if code := doBuild(cfg); code != 0 {
				return code
			}
		} else {
			if code := doDeploy(cfg, isoSource); code != 0 {
				return code
			}
		}
		pveCleanOldVMs(cfg)

		mac := vmMAC(cfg.VMID)
		log(fmt.Sprintf("Creating VM %s (MAC %s)...", cfg.VMID, mac))
		// SeaBIOS (the default, no --bios flag needed) boots via MBR/BIOS GRUB.
		// installed.nix uses hybrid GRUB: BIOS stage installed to the 1MiB bios_grub
		// partition + EFI fallback at EFI/BOOT/BOOTX64.EFI for real UEFI hardware.
		// --sata0: AHCI SATA disk; SeaBIOS boots it via the standard BIOS boot path.
		// Boot order: sata0 first, ide2 (ISO) as fallback. On first boot the disk is
		// empty so BIOS falls back to the ISO. After installation the HD wins
		// automatically — no boot order update needed after install.
		// A fixed MAC (derived from VMID) keeps DHCP from allocating a new lease on
		// every destroy/recreate cycle.
		createCmd := fmt.Sprintf(
			"qm create %s --name %s --memory %d --cores %d --ostype l26 --cdrom 'local:iso/%s' --boot 'order=sata0;ide2' --net0 'virtio,bridge=vmbr0,macaddr=%s' --agent enabled=1 --serial0 socket --sata0 %s:%d",
			cfg.VMID, pveVMName, cfg.Memory, cfg.Cores, pveISOFilename, mac, pveDiskStorage, pveDiskSizeGB,
		)
		if err := pveExecStream(cfg, createCmd); err != nil {
			errorf("Failed to create VM: %v", err)
			return 1
		}

		log("Starting VM...")
		if err := pveExecStream(cfg, fmt.Sprintf("qm start %s", cfg.VMID)); err != nil {
			errorf("Failed to start VM: %v", err)
			return 1
		}

		// Phase 1: Wait for the live installer ISO to boot (root, empty password)
		vmIP = waitForISOReady(cfg)
		if vmIP == "" {
			errorf("Timeout: ISO did not become reachable via SSH within %ds", pveBootTimeout)
			return 1
		}

		if !autoInstall {
			// Manual install mode: leave the live ISO running for the user.
			fmt.Println()
			log(fmt.Sprintf("Live ISO is up at %s", vmIP))
			fmt.Printf("  SSH:        ssh root@%s  (empty password)\n", vmIP)
			fmt.Printf("  Installer:  http://%s:%s\n", vmIP, pveInstallerPort)
			fmt.Println()
			fmt.Println("  Install manually, then run: ./bloud checks")
			fmt.Println("  To tear down:               ./bloud destroy")
			return 0
		}

		// Phase 2: Drive the installer API — partition, install, reboot
		if code := runInstaller(cfg, vmIP); code != 0 {
			return code
		}
	}

	// Phase 3: Wait for the installed system (bloud user, bloud password).
	// Runs after --install or --skip-deploy (assumes system already installed).
	log("Waiting for installed system to come up...")
	vmIP = waitForPVEVMReady(cfg)
	if vmIP == "" {
		errorf("Timeout: installed system did not become reachable via SSH within %ds", pveBootTimeout)
		return 1
	}

	// Stream journal in background while waiting for services (warnings/errors only to reduce noise)
	log(fmt.Sprintf("Waiting for Bloud services (timeout: %ds)...", pveServiceTimeout))
	fmt.Println()

	ctx, cancelJournal := context.WithCancel(context.Background())
	go func() {
		c := exec.CommandContext(ctx, "sshpass", "-p", pveVMSSHPass,
			"ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			pveVMSSHUser+"@"+vmIP,
			"journalctl --follow --no-pager -o short-iso -p warning",
		)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		_ = c.Run()
	}()

	// Poll for services
	servicesUp := false
	for i := 0; i < pveServiceTimeout; i++ {
		out, _ := vmExec(vmIP, "curl -sf http://localhost:3000/api/health")
		if strings.Contains(out, "ok") {
			servicesUp = true
			break
		}
		time.Sleep(1 * time.Second)
	}

	cancelJournal()
	time.Sleep(200 * time.Millisecond) // let the goroutine flush

	if servicesUp {
		fmt.Println()
		log("Services are up")
	} else {
		fmt.Println()
		warn("Timeout waiting for services — running checks anyway")
	}

	passed, failed := runPVEChecks(vmIP)

	// Extra diagnostics
	fmt.Println()
	log("Container status:")
	_ = vmExecStream(vmIP, `podman ps --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}'`)

	printPVEResults(vmIP, passed, failed)
	printVMInfo()

	if failed > 0 {
		return 1
	}
	return 0
}


func cmdStopPVE() int {
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		log("VM is not running")
		return 0
	}
	log(fmt.Sprintf("Stopping VM %s...", cfg.VMID))
	if err := pveExecStream(cfg, fmt.Sprintf("qm stop %s", cfg.VMID)); err != nil {
		errorf("Failed to stop VM: %v", err)
		return 1
	}
	log("VM stopped")
	return 0
}

func cmdDestroyPVE() int {
	cfg := getPVEConfig()
	if !pveVMExists(cfg) {
		log(fmt.Sprintf("VM %s does not exist", cfg.VMID))
		return 0
	}
	pveDestroyVM(cfg)
	return 0
}

func cmdStatusPVE() int {
	cfg := getPVEConfig()
	fmt.Println()
	fmt.Printf("  Backend:  %sProxmox%s (%s)\n", colorCyan, colorReset, cfg.Host)
	fmt.Printf("  VMID:     %s\n", cfg.VMID)
	fmt.Println()

	if !pveVMExists(cfg) {
		fmt.Printf("  VM:       %sNot created%s\n", colorRed, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start [iso]' to deploy and boot a VM")
		return 0
	}

	if !pveVMIsRunning(cfg) {
		fmt.Printf("  VM:       %sStopped%s\n", colorYellow, colorReset)
		fmt.Println()
		fmt.Println("  Run './bloud start --skip-deploy' to boot the existing VM")
		return 0
	}

	fmt.Printf("  VM:       %sRunning%s\n", colorGreen, colorReset)

	ip := getVMIP(cfg)
	if ip == "" {
		fmt.Printf("  IP:       %sUnknown (no guest agent?)\n%s", colorYellow, colorReset)
	} else {
		fmt.Printf("  IP:       %s%s%s\n", colorGreen, ip, colorReset)
	}

	if ip != "" {
		fmt.Println()
		log("Service status:")

		for _, name := range []string{"bloud-host-agent", "bloud-apps.target", "avahi-daemon"} {
			scope := "--user"
			if name == "bloud-host-agent" || name == "avahi-daemon" {
				scope = ""
			}
			out, _ := vmExec(ip, fmt.Sprintf("systemctl %s is-active %s.service 2>/dev/null || systemctl %s is-active %s 2>/dev/null", scope, name, scope, name))
			state := strings.TrimSpace(out)
			color := colorRed
			if state == "active" {
				color = colorGreen
			}
			fmt.Printf("  %-30s %s%s%s\n", name, color, state, colorReset)
		}

		out, _ := vmExec(ip, "curl -sf http://localhost:3000/api/health 2>/dev/null")
		if strings.Contains(out, "ok") {
			fmt.Printf("  %-30s %srunning%s\n", "host-agent API", colorGreen, colorReset)
		} else {
			fmt.Printf("  %-30s %snot responding%s\n", "host-agent API", colorYellow, colorReset)
		}
	}

	fmt.Println()
	return 0
}

func cmdLogsPVE() int {
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Start with: ./bloud start [iso]")
		return 1
	}
	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP (is the guest agent running?)")
		return 1
	}
	log("Streaming VM journal (Ctrl-C to stop)...")
	fmt.Println()
	_ = vmExecStream(ip, "journalctl --follow --no-pager -o short-iso")
	return 0
}

func cmdShellPVE(args []string) int {
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Start with: ./bloud start [iso]")
		return 1
	}
	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP (is the guest agent running?)")
		return 1
	}
	cmd := strings.Join(args, " ")
	if err := vmInteractive(ip, cmd); err != nil {
		if cmd == "" {
			return 0
		}
		errorf("Command failed: %v", err)
		return 1
	}
	return 0
}

func cmdChecksPVE() int {
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Start with: ./bloud start [iso]")
		return 1
	}
	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP (is the guest agent running?)")
		return 1
	}
	passed, failed := runPVEChecks(ip)
	printPVEResults(ip, passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

func cmdInstallPVE(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud install <app-name>")
		return 1
	}
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Start with: ./bloud start [iso]")
		return 1
	}
	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP")
		return 1
	}
	appName := args[0]
	log(fmt.Sprintf("Installing %s...", appName))
	out, err := vmExec(ip, fmt.Sprintf(
		`curl -s -X POST -w "\n%%{http_code}" http://localhost:3000/api/apps/%s/install`, appName,
	))
	if err != nil {
		errorf("Failed to call install API: %v", err)
		return 1
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	httpCode := lines[len(lines)-1]
	body := strings.Join(lines[:len(lines)-1], "\n")
	if httpCode != "200" && httpCode != "201" {
		errorf("Install failed (HTTP %s): %s", httpCode, body)
		return 1
	}
	log(fmt.Sprintf("Successfully installed %s", appName))
	fmt.Println(body)
	return 0
}

func cmdUninstallPVE(args []string) int {
	if len(args) < 1 {
		errorf("Usage: ./bloud uninstall <app-name>")
		return 1
	}
	cfg := getPVEConfig()
	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Start with: ./bloud start [iso]")
		return 1
	}
	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP")
		return 1
	}
	appName := args[0]
	log(fmt.Sprintf("Uninstalling %s...", appName))
	out, err := vmExec(ip, fmt.Sprintf(
		`curl -s -X POST -w "\n%%{http_code}" http://localhost:3000/api/apps/%s/uninstall`, appName,
	))
	if err != nil {
		errorf("Failed to call uninstall API: %v", err)
		return 1
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	httpCode := lines[len(lines)-1]
	body := strings.Join(lines[:len(lines)-1], "\n")
	if httpCode != "200" {
		errorf("Uninstall failed (HTTP %s): %s", httpCode, body)
		return 1
	}
	log(fmt.Sprintf("Successfully uninstalled %s", appName))
	fmt.Println(body)
	return 0
}

// ── Fast iteration commands ────────────────────────────────────────────────────

// cmdRebuildPVE syncs the NixOS configuration from the local project to a running
// installed VM and applies it via nixos-rebuild switch. Avoids a full ISO rebuild
// by patching the live system in place (~2-3 minutes vs ~15 minutes).
//
// Flow:
//  1. Initialize /tmp/bloud-src on VM from the currently deployed Nix store
//     (gives Nix the pre-built binary + frontend without rebuilding them)
//  2. Rsync local nixos/ and apps/ on top (only changed files transfer)
//  3. nixos-rebuild switch --flake path:/tmp/bloud-src#bloud --impure
//  4. Wait for host-agent health + run checks
func cmdRebuildPVE() int {
	cfg := getPVEConfig()

	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Run './bloud start --install' first to create an installed system.")
		return 1
	}

	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP (is the QEMU guest agent running?)")
		return 1
	}

	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Step 1: Initialize /tmp/bloud-src on the VM from the currently deployed
	// Nix store path. We read the binary path from the main unit file (not the
	// drop-in, which may point to /tmp/host-agent-push after a ./bloud push).
	// The store path gives us the pre-built binary + frontend + NixOS modules,
	// so Nix can reuse the existing derivation for a config-only change.
	log("Initializing " + pveSyncDir + " from deployed store...")
	initScript := `set -e
UNIT_PATH=$(systemctl show bloud-host-agent.service -p FragmentPath --value)
AGENT_BIN=$(grep '^ExecStart=' "$UNIT_PATH" | head -1 | sed 's/^ExecStart=//')
if [ -z "$AGENT_BIN" ] || [[ "$AGENT_BIN" != /nix/store/* ]]; then
  echo "ERROR: Could not find Nix store binary in $UNIT_PATH" >&2
  echo "  ExecStart: $AGENT_BIN" >&2
  exit 1
fi
PKG=$(echo "$AGENT_BIN" | sed 's|/bin/host-agent||')
SRC="$PKG/share/bloud"
echo "==> Source: $SRC"
sudo rm -rf ` + pveSyncDir + `
cp -r "$SRC" ` + pveSyncDir + `
chmod -R u+w ` + pveSyncDir + `
mkdir -p ` + pveSyncDir + `/build
cp "$AGENT_BIN" ` + pveSyncDir + `/build/host-agent
chmod +x ` + pveSyncDir + `/build/host-agent
cp -r "$PKG/share/bloud/web/build" ` + pveSyncDir + `/build/frontend
echo "==> ` + pveSyncDir + ` ready"`

	if err := vmExecStream(ip, initScript); err != nil {
		errorf("Failed to initialize %s on VM: %v", pveSyncDir, err)
		return 1
	}

	// Step 2: Rsync the local nixos/ and apps/ directories onto the VM,
	// overwriting what was copied from the store. rsync only transfers diffs.
	log("Syncing NixOS configuration...")
	sshCmd := fmt.Sprintf("sshpass -p %s ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR", pveVMSSHPass)

	for _, dir := range []struct{ local, remote string }{
		{filepath.Join(root, "nixos") + "/", pveVMSSHUser + "@" + ip + ":" + pveSyncDir + "/nixos/"},
		{filepath.Join(root, "apps") + "/", pveVMSSHUser + "@" + ip + ":" + pveSyncDir + "/apps/"},
	} {
		c := exec.Command("rsync", "-av", "-e", sshCmd, dir.local, dir.remote)
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			errorf("rsync failed for %s: %v", dir.local, err)
			return 1
		}
	}

	// Sync flake root files (flake.nix and flake.lock only).
	c := exec.Command("rsync", "-av", "-e", sshCmd,
		"--include=flake.nix", "--include=flake.lock", "--exclude=*",
		root+"/",
		pveVMSSHUser+"@"+ip+":"+pveSyncDir+"/",
	)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		errorf("rsync of flake files failed: %v", err)
		return 1
	}

	// Step 3: Run nixos-rebuild switch on the VM.
	// path: forces Nix to evaluate the flake from the directory (not treat it as a store path).
	// --impure allows access to /tmp/bloud-src without a git repo present.
	log("Running nixos-rebuild switch (1-3 minutes)...")
	fmt.Println()
	rebuildCmd := "sudo /run/current-system/sw/bin/nixos-rebuild switch" +
		" --flake path:" + pveSyncDir + "#bloud --impure 2>&1"
	if err := vmExecStream(ip, rebuildCmd); err != nil {
		errorf("nixos-rebuild failed: %v", err)
		return 1
	}

	// Step 4: Wait for the host-agent to come back up after the switch.
	fmt.Println()
	log("Waiting for host-agent to come up...")
	for i := 0; i < 60; i++ {
		out, _ := vmExec(ip, "curl -sf http://localhost:3000/api/health 2>/dev/null")
		if strings.Contains(out, "ok") {
			break
		}
		if i > 0 && i%10 == 0 {
			fmt.Printf("  ... waiting (%d/60s)\n", i)
		}
		time.Sleep(1 * time.Second)
	}

	passed, failed := runPVEChecks(ip)
	printPVEResults(ip, passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// cmdPushPVE cross-compiles the host-agent binary for linux/amd64 and hot-swaps
// it on the running VM via a systemd drop-in override, bypassing Nix entirely.
// This is the fastest path for testing Go code changes (~30s end-to-end).
//
// The drop-in at /etc/systemd/system/bloud-host-agent.service.d/dev-override.conf
// overrides ExecStart to point to /tmp/host-agent-push. All other service settings
// (env vars, user, restart policy) are inherited from the main unit.
//
// Note: the override persists across restarts but NOT across nixos-rebuild switch,
// which regenerates the unit file and drops the override directory.
func cmdPushPVE() int {
	cfg := getPVEConfig()

	if !pveVMIsRunning(cfg) {
		errorf("VM is not running. Run './bloud start --install' first.")
		return 1
	}

	ip := getVMIP(cfg)
	if ip == "" {
		errorf("Could not get VM IP (is the QEMU guest agent running?)")
		return 1
	}

	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	// Cross-compile the host-agent binary for linux/amd64.
	// Go cross-compilation is hermetic (CGO_ENABLED=0) and typically takes ~5s.
	log("Building host-agent (linux/amd64)...")
	localBinary := "/tmp/bloud-push-binary"
	buildCmd := exec.Command("go", "build", "-o", localBinary, "./cmd/host-agent")
	buildCmd.Dir = filepath.Join(root, "services", "host-agent")
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		errorf("Build failed: %v", err)
		return 1
	}
	defer os.Remove(localBinary)

	// Upload the binary to the VM.
	log("Uploading binary to VM...")
	scpCmd := exec.Command("sshpass", "-p", pveVMSSHPass,
		"scp",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		localBinary,
		pveVMSSHUser+"@"+ip+":/tmp/host-agent-push",
	)
	scpCmd.Stdout = os.Stdout
	scpCmd.Stderr = os.Stderr
	if err := scpCmd.Run(); err != nil {
		errorf("Failed to upload binary: %v", err)
		return 1
	}

	// Create/update a systemd drop-in that overrides ExecStart, then restart.
	// The empty ExecStart= line clears the inherited value before setting the new
	// one — systemd requires this pattern for ExecStart overrides.
	log("Installing binary override and restarting service...")
	// Use /run/systemd/system/ for drop-ins — /etc/systemd/system/ is read-only
	// on NixOS (Nix-managed). /run is a tmpfs that's always writable.
	installScript := `set -e
chmod +x /tmp/host-agent-push
sudo mkdir -p /run/systemd/system/bloud-host-agent.service.d
printf '[Service]\nExecStart=\nExecStart=/tmp/host-agent-push\n' \
  | sudo tee /run/systemd/system/bloud-host-agent.service.d/dev-override.conf > /dev/null
sudo systemctl daemon-reload
sudo systemctl restart bloud-host-agent.service
echo "Service restarted with pushed binary"`

	if err := vmExecStream(ip, installScript); err != nil {
		errorf("Failed to install override: %v", err)
		return 1
	}

	// Poll for health before running full checks.
	log("Waiting for service to come up...")
	for i := 0; i < 30; i++ {
		out, _ := vmExec(ip, "curl -sf http://localhost:3000/api/health 2>/dev/null")
		if strings.Contains(out, "ok") {
			break
		}
		time.Sleep(1 * time.Second)
	}

	passed, failed := runPVEChecks(ip)
	printPVEResults(ip, passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// cmdSnapshotPVE manages Proxmox VM snapshots for the test VM.
// Snapshots let you restore a clean installed state (~15s) without a full ISO reinstall.
//
// Typical workflow:
//
//	./bloud snapshot save          # Save state right after fresh install
//	./bloud rebuild                # Iterate on NixOS config
//	./bloud snapshot restore       # Reset to clean state when needed
func cmdSnapshotPVE(args []string) int {
	cfg := getPVEConfig()

	if len(args) == 0 {
		errorf("Usage: ./bloud snapshot <save|restore|list> [name]")
		return 1
	}

	action := args[0]
	name := "base-installed"
	if len(args) > 1 {
		name = args[1]
	}

	switch action {
	case "save":
		if !pveVMExists(cfg) {
			errorf("VM %s does not exist", cfg.VMID)
			return 1
		}
		log(fmt.Sprintf("Saving snapshot '%s'...", name))
		if err := pveExecStream(cfg, fmt.Sprintf(
			"qm snapshot %s %s --description 'bloud dev snapshot'", cfg.VMID, name,
		)); err != nil {
			errorf("Failed to save snapshot: %v", err)
			return 1
		}
		log(fmt.Sprintf("Snapshot '%s' saved", name))
		fmt.Printf("  Restore with: ./bloud snapshot restore %s\n", name)

	case "restore":
		if !pveVMExists(cfg) {
			errorf("VM %s does not exist", cfg.VMID)
			return 1
		}
		if pveVMIsRunning(cfg) {
			log("Stopping VM before snapshot restore...")
			if _, err := pveExec(cfg, fmt.Sprintf("qm stop %s", cfg.VMID)); err != nil {
				errorf("Failed to stop VM: %v", err)
				return 1
			}
			for i := 0; i < 30; i++ {
				if !pveVMIsRunning(cfg) {
					break
				}
				time.Sleep(1 * time.Second)
			}
		}
		log(fmt.Sprintf("Rolling back to snapshot '%s'...", name))
		if err := pveExecStream(cfg, fmt.Sprintf("qm rollback %s %s", cfg.VMID, name)); err != nil {
			errorf("Failed to restore snapshot: %v", err)
			return 1
		}
		log("Starting VM...")
		if err := pveExecStream(cfg, fmt.Sprintf("qm start %s", cfg.VMID)); err != nil {
			errorf("Failed to start VM after restore: %v", err)
			return 1
		}
		log("VM started")
		fmt.Printf("  Stream logs: ./bloud logs\n")
		fmt.Printf("  SSH in:      ./bloud shell\n")
		fmt.Printf("  Run checks:  ./bloud checks\n")

	case "list":
		if !pveVMExists(cfg) {
			errorf("VM %s does not exist", cfg.VMID)
			return 1
		}
		out, err := pveExec(cfg, fmt.Sprintf("qm listsnapshots %s", cfg.VMID))
		if err != nil {
			errorf("Failed to list snapshots: %v", err)
			return 1
		}
		fmt.Println(out)

	default:
		errorf("Unknown action '%s'. Use: save, restore, list", action)
		return 1
	}

	return 0
}
