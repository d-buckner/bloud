package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	pveDefaultHost    = "root@192.168.0.62"
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
	host := os.Getenv("BLOUD_PVE_HOST")
	if host == "" {
		host = pveDefaultHost
	}
	vmid := os.Getenv("BLOUD_PVE_VMID")
	if vmid == "" {
		vmid = pveDefaultVMID
	}
	return pveConfig{
		Host:   host,
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

// ── Commands ───────────────────────────────────────────────────────────────────

// cmdStartPVE is the main ISO test lifecycle:
// deploy ISO → clean old VMs → create VM → boot → wait for services → checks → destroy
// Flags: --keep (don't destroy), --skip-deploy (reuse existing VM)
func cmdStartPVE(args []string) int {
	cfg := getPVEConfig()
	keep := false
	skipDeploy := false
	isoSource := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--keep":
			keep = true
		case "--skip-deploy":
			skipDeploy = true
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

	cleanup := func() {
		if keep {
			warn(fmt.Sprintf("Keeping VM %s running (--keep). IP shown above.", cfg.VMID))
			warn(fmt.Sprintf("Teardown: ssh %s 'qm stop %s && qm destroy %s --purge'", cfg.Host, cfg.VMID, cfg.VMID))
			return
		}
		if !skipDeploy {
			pveDestroyVM(cfg)
		}
	}

	if !skipDeploy {
		if code := doDeploy(cfg, isoSource); code != 0 {
			return code
		}
		pveCleanOldVMs(cfg)

		log(fmt.Sprintf("Creating VM %s...", cfg.VMID))
		createCmd := fmt.Sprintf(
			"qm create %s --name %s --memory %d --cores %d --ostype l26 --cdrom 'local:iso/%s' --boot 'order=ide2' --net0 'virtio,bridge=vmbr0' --agent enabled=1 --serial0 socket",
			cfg.VMID, pveVMName, cfg.Memory, cfg.Cores, pveISOFilename,
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
	}

	// Wait for VM to boot and accept SSH
	vmIP := waitForPVEVMReady(cfg)
	if vmIP == "" {
		errorf("Timeout: VM did not become reachable via SSH within %ds", pveBootTimeout)
		cleanup()
		return 1
	}

	// Stream journal in background while waiting for services
	log(fmt.Sprintf("Waiting for Bloud services (timeout: %ds)...", pveServiceTimeout))
	log("Streaming VM journal...")
	fmt.Println()

	ctx, cancelJournal := context.WithCancel(context.Background())
	go func() {
		c := exec.CommandContext(ctx, "sshpass", "-p", pveVMSSHPass,
			"ssh",
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
			"-o", "LogLevel=ERROR",
			pveVMSSHUser+"@"+vmIP,
			"journalctl --follow --no-pager -o short-iso",
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
	cleanup()

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
