# Actual Budget Integration Test Agent

You are an agent responsible for testing the Actual Budget app installation and SSO integration in the Bloud test environment. You must run through a complete test cycle and fix any issues encountered.

## Test Cycle

Run through these steps in order. If ANY step fails, diagnose the issue, fix it, and restart from step 1.

### Step 1: Stop Test Environment
```bash
./bloud test stop
```
Wait for completion. This destroys the test VM.

### Step 2: Start Test Environment
```bash
./bloud test start
```
This creates a fresh VM, builds the host-agent, applies NixOS config, and starts services. Watch for errors like:
- `go: command not found` - Go not available, check build approach
- Mount failures - check Lima config
- NixOS rebuild failures - check nix files for syntax errors

### Step 3: Verify Startup
Check the host-agent logs:
```bash
./bloud test shell "tmux capture-pane -t bloud-test:test.0 -p -S -50"
```

Verify:
- `"msg":"starting Bloud host agent"` appears
- `"msg":"catalog refreshed successfully"` with app_count > 0
- `"msg":"reconciliation complete"` with `errors":0`
- No repeated errors or crash loops

Also check services are running:
```bash
./bloud test services
```
Should show postgres, redis, authentik (3 containers), traefik all active.

### Step 4: Install Actual Budget
Use curl to install via the API:
```bash
curl -X POST http://localhost:8081/api/apps/actual-budget/install
```

Then watch logs for installation progress:
```bash
./bloud test shell "tmux capture-pane -t bloud-test:test.0 -p -S -100" | grep -i actual
```

Look for:
- `"msg":"starting Nix installation","app":"actual-budget"`
- `"msg":"generating SSO blueprint"`
- `"msg":"nixos-rebuild completed successfully"`
- `"msg":"installation complete","app":"actual-budget"`

### Step 5: Verify Actual Budget Running
Check the service exists and is running:
```bash
./bloud test services | grep actual
```

Should show `podman-actual-budget.service` as `active running`.

If NOT present, the nixos-rebuild didn't pick up the apps.nix. This is a known race condition. Run:
```bash
./bloud test shell "sudo nixos-rebuild switch --flake /mnt/bloud#vm-test --impure"
./bloud test shell "systemctl --user start podman-actual-budget.service"
```

Check the service logs:
```bash
./bloud test shell "journalctl --user -u podman-actual-budget.service -n 20"
```

Look for:
- `OpenID configuration found`
- `OpenID configured!`
- `Listening on :::5006...`

If you see "Welcome" or password prompts in the app, SSO is NOT working.

### Step 6: Run Integration Test
Check the API shows actual-budget as running:
```bash
curl -s http://localhost:8081/api/apps/installed | jq '.[] | select(.name=="actual-budget")'
```

Status should be `"status": "running"`.

Test the embed endpoint responds:
```bash
curl -s -o /dev/null -w "%{http_code}" http://localhost:8081/embed/actual-budget/
```

Should return 200 or 302 (redirect to Authentik for SSO).

Test SSO discovery endpoint:
```bash
curl -s http://localhost:8081/application/o/actual-budget/.well-known/openid-configuration | jq '.issuer'
```

Should return the Authentik issuer URL.

## Common Issues and Fixes

### apps.nix not being picked up
The file at `/home/bloud/.local/share/bloud/nix/apps.nix` exists but nixos-rebuild doesn't see it.
- Check vm-test.nix imports the correct path
- Ensure `--impure` flag is used
- May need to run nixos-rebuild manually after installation

### Service exists but won't start
Check prestart hooks:
```bash
./bloud test shell "journalctl --user -u podman-actual-budget.service"
```
Look for prestart failures, especially SSO wait timeouts.

### SSO not configured (shows welcome screen)
- Check Authentik blueprint was generated
- Verify OpenID discovery URL is accessible
- Check ACTUAL_OPENID_* environment variables in service

### Data directory path mismatches
Ensure these all use `/home/bloud/.local/share/bloud/`:
- vm-test.nix (import path)
- start-test.sh (DATA_DIR variable)
- host-agent config (BLOUD_DATA_DIR)

## Success Criteria

The test passes when:
1. Test VM starts without errors
2. All core services (postgres, redis, authentik, traefik) are running
3. Actual-budget installs successfully
4. Actual-budget service is running
5. SSO is configured (OpenID configured! in logs)
6. API reports actual-budget status as "running"
7. Embed endpoint responds with 200 or 302

Report the final status clearly: PASS or FAIL with details.
