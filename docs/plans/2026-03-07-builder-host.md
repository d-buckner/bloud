# Builder Host Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the broken Proxmox-managed builder VM (VMID 9998) with `BLOUD_BUILDER_HOST` — a direct SSH target for building ISOs on any pre-existing host with Nix installed.

**Architecture:** All builder VM infrastructure in `cli/pve.go` is removed. `doBuild()` reads `BLOUD_BUILDER_HOST` from env, SSHes directly with the default key, rsyncs source, and runs the build. `setup-builder` provisions the target host with Go + Node via `nix profile install`.

**Tech Stack:** Go (CLI), SSH, rsync, Nix flakes

---

### Task 1: Remove all Proxmox builder VM constants and helpers

**Files:**
- Modify: `cli/pve.go`

Remove the following constants (lines ~36-47):
```go
pveBuildVMID      = "9998"
pveBuildVMName    = "bloud-builder"
pveBuildMemory    = 8192
pveBuildCores     = 4
pveBuildDisk      = "40G"
pveBuildDir       = "/root/bloud"
pveBuildImageURL  = "https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img"
pveBuildImageFile = "noble-server-cloudimg-amd64.img"
pveBuildKeyPath   = ".bloud/builder_rsa" // relative to $HOME
```

Remove the following functions entirely:
- `builderCfg(cfg pveConfig) pveConfig`
- `builderKeyPaths() (private, public string)`
- `ensureBuilderKey() error`
- `builderExec(ip, cmd string) (string, error)`
- `builderExecStream(ip, cmd string) error`
- `waitForBuilderSSH(bc pveConfig) string`
- `createBuilderVM(cfg pveConfig) int`
- `doConfigureBuilder(ip string) int`

**Step 1: Delete those constants and functions from `cli/pve.go`**

After deletion, verify the file compiles:
```bash
cd cli && go build ./...
```
Expected: compile errors referencing the removed symbols (they're still called from `doBuild` and `cmdSetupBuilderPVE` — that's fine, fix in later tasks).

**Step 2: Commit the deletions**
```bash
git add cli/pve.go
git commit -m "refactor: remove Proxmox-managed builder VM infrastructure"
```

---

### Task 2: Remove `destroy-builder` command

**Files:**
- Modify: `cli/main.go`
- Modify: `cli/pve.go`

**Step 1: Remove from `main.go` switch**

Delete this case from the switch in `main()`:
```go
case "destroy-builder":
    if isPVEMode() {
        exitCode = cmdDestroyBuilderPVE()
    } else {
        fmt.Fprintf(os.Stderr, "%sError:%s 'destroy-builder' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
        exitCode = 1
    }
```

**Step 2: Remove `cmdDestroyBuilderPVE` from `pve.go`**

Delete:
```go
func cmdDestroyBuilderPVE() int {
    cfg := getPVEConfig()
    bc := builderCfg(cfg)
    if !pveVMExists(bc) {
        log(fmt.Sprintf("Build VM %s does not exist", pveBuildVMID))
        return 0
    }
    pveDestroyVM(bc)
    return 0
}
```

**Step 3: Compile check**
```bash
cd cli && go build ./...
```
Expected: still errors from `doBuild` and `cmdSetupBuilderPVE` calling removed code — that's expected.

**Step 4: Commit**
```bash
git add cli/main.go cli/pve.go
git commit -m "refactor: remove destroy-builder command"
```

---

### Task 3: Add direct SSH helpers and `getBuilderHost`

**Files:**
- Modify: `cli/pve.go`

Add these three functions near the top of the builder section (after the `pveSyncDir` constant block):

```go
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
```

**Step 1: Add the three functions to `cli/pve.go`**

**Step 2: Compile check**
```bash
cd cli && go build ./...
```
Expected: still errors from `doBuild` and `cmdSetupBuilderPVE` — that's fine.

**Step 3: Commit**
```bash
git add cli/pve.go
git commit -m "feat: add direct SSH builder helpers"
```

---

### Task 4: Rewrite `doBuild()`

**Files:**
- Modify: `cli/pve.go`

Replace the entire `doBuild(cfg pveConfig) int` function with:

```go
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
		"--exclude=build/",
		"--exclude=node_modules/",
		"--exclude=.direnv/",
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
git add -f build/

echo '==> Building ISO...'
nix build .#packages.x86_64-linux.iso --no-link
echo 'Build complete.'`

	if err := builderSSHExecStream(host, buildScript); err != nil {
		errorf("ISO build failed: %v", err)
		return 1
	}

	// Get the store path (instant — build already done above)
	storePath, err := builderSSHExec(host,
		`export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH"; `+
			`cd ~/bloud && nix build .#packages.x86_64-linux.iso --no-link --print-out-paths`,
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
```

**Step 1: Replace `doBuild` in `cli/pve.go`**

**Step 2: Compile check**
```bash
cd cli && go build ./...
```
Expected: still one error from `cmdSetupBuilderPVE` — fix in next task.

**Step 3: Commit**
```bash
git add cli/pve.go
git commit -m "feat: rewrite doBuild() to use BLOUD_BUILDER_HOST direct SSH"
```

---

### Task 5: Rewrite `cmdSetupBuilderPVE()`

**Files:**
- Modify: `cli/pve.go`

Replace the entire `cmdSetupBuilderPVE() int` function with:

```go
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
	if _, err := builderSSHExec(host, "command -v nix"); err != nil {
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
```

**Step 1: Replace `cmdSetupBuilderPVE` in `cli/pve.go`**

**Step 2: Compile check — must pass cleanly**
```bash
cd cli && go build ./...
```
Expected: zero errors.

**Step 3: Commit**
```bash
git add cli/pve.go
git commit -m "feat: rewrite setup-builder to provision BLOUD_BUILDER_HOST via Nix"
```

---

### Task 6: Update help text

**Files:**
- Modify: `cli/main.go`

In `printUsage()`, in the PVE mode block, update the builder-related lines:

**Remove:**
```
  setup-builder         Provision or update the ISO build VM (VMID 9998)
  destroy-builder       Destroy the ISO build VM
```

**Replace with:**
```
  setup-builder         Provision BLOUD_BUILDER_HOST with Go + Node via Nix
```

Also update the Environment section — add:
```
  BLOUD_BUILDER_HOST    SSH target for local ISO builds (e.g. builder@192.168.0.105)
```

And update the examples:
- Change: `./bloud start --build             # build ISO on builder VM and boot`
- To: `./bloud start --build             # build ISO on BLOUD_BUILDER_HOST and boot`

**Step 1: Make those text changes in `cli/main.go`**

**Step 2: Compile check**
```bash
cd cli && go build ./...
```
Expected: zero errors.

**Step 3: Rebuild the CLI binary**
```bash
npm run setup
```

**Step 4: Verify help output**
```bash
./bloud help
```
Expected: no mention of `destroy-builder` or VMID 9998. `setup-builder` and `BLOUD_BUILDER_HOST` visible.

**Step 5: Commit**
```bash
git add cli/main.go
git commit -m "docs: update help text for BLOUD_BUILDER_HOST builder"
```

---

### Task 7: Add `BLOUD_BUILDER_HOST` to `.env` and provision the builder

**Files:**
- Modify: `.env`

**Step 1: Add to `.env`**
```
BLOUD_BUILDER_HOST=builder@192.168.0.105
```

**Step 2: Run setup-builder**
```bash
./bloud setup-builder
```
Expected output:
```
==> Checking builder host (builder@192.168.0.105)...
==> Provisioning builder with Go and Node.js via Nix...
==> Installing git and rsync...
==> Installing Go and Node.js via nix profile...
go version go1.x.x linux/amd64
vXX.X.X
Provisioning complete.
==> Builder provisioned successfully
  Run './bloud start --build' to build and test the ISO
```

If it fails: check that your SSH key is installed on the builder (`ssh builder@192.168.0.105` should work without a password prompt).

**Step 3: Commit**
```bash
git add .env
git commit -m "chore: configure BLOUD_BUILDER_HOST for local ISO builds"
```

Note: `.env` is in `.gitignore` — this commit will only stage if you force-add it. If `.env` is gitignored (check with `git status`), skip committing `.env` and just leave it as a local file.

---

### Task 8: End-to-end test

**Step 1: Run full build + install**
```bash
./bloud start --build --install
```

Expected phases:
1. Source syncs to builder via rsync
2. Go binaries build on builder
3. Frontend builds on builder
4. ISO builds via `nix build` (15-30 min first time, faster after Nix cache warms)
5. ISO downloads to `/tmp/bloud-built.iso`
6. ISO uploads to Proxmox
7. Test VM created, boots from ISO
8. Installer runs automatically
9. VM reboots into installed system
10. Health checks pass

**If nix build fails** on the builder:
- SSH in and check: `ssh builder@192.168.0.105`
- Check Nix is on PATH: `export PATH="$HOME/.nix-profile/bin:/nix/var/nix/profiles/default/bin:$PATH" && nix --version`
- Try manually: `cd ~/bloud && nix build .#packages.x86_64-linux.iso`

**If health checks fail**, use the normal debugging workflow:
```bash
./bloud logs     # stream journal
./bloud shell    # SSH into installed VM
./bloud checks   # re-run health checks
```
