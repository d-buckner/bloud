# `./bloud smoke` Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `./bloud smoke` command that builds a fresh ISO, deploys it to Proxmox, completes the setup wizard, installs Miniflux, and validates the Bloud shell embedding with a visual regression screenshot.

**Architecture:** Standalone `smoke/` directory with its own Playwright config targeting `http://bloud.local`. A new `cmdSmokePVE` function in `cli/pve.go` calls the existing `cmdStartPVE` with `["--build", "--install"]`, then shells out to `npx playwright test` in `smoke/`. No global setup/teardown — the CLI handles all VM lifecycle.

**Tech Stack:** TypeScript, Playwright `^1.49.0`, Go (CLI additions)

---

## Chunk 1: smoke/ directory — scaffolding, API client, test

### Task 1: Scaffold smoke/ directory

**Files:**
- Create: `smoke/.gitignore`
- Create: `smoke/package.json`
- Create: `smoke/tsconfig.json`
- Create: `smoke/playwright.config.ts`

- [ ] **Step 1: Create `smoke/.gitignore`**

```
node_modules/
test-results/
playwright-report/
.playwright-mcp/
```

- [ ] **Step 2: Create `smoke/package.json`**

```json
{
  "name": "@bloud/smoke-tests",
  "version": "0.1.0",
  "license": "AGPL-3.0-only",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "playwright test",
    "test:update": "playwright test --update-snapshots",
    "install-browsers": "npx playwright install chromium"
  },
  "devDependencies": {
    "@playwright/test": "^1.49.0",
    "@types/node": "^22.0.0",
    "typescript": "^5.0.0"
  }
}
```

- [ ] **Step 3: Create `smoke/tsconfig.json`**

```json
{
  "compilerOptions": {
    "target": "ESNext",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true
  },
  "include": ["**/*.ts"]
}
```

- [ ] **Step 4: Create `smoke/playwright.config.ts`**

```typescript
import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  workers: 1,
  reporter: [
    ['html', { open: 'never' }],
    ['list'],
  ],
  use: {
    baseURL: 'http://bloud.local',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    actionTimeout: 30_000,
    navigationTimeout: 60_000,
  },
  // 15 minutes: waitForSetupReady (5 min) + waitForAppRunning (5 min) + buffer
  timeout: 900_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
    },
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  outputDir: 'test-results',
});
```

- [ ] **Step 5: Install dependencies and Playwright browser**

Run from `smoke/`:
```bash
cd smoke && npm ci && npx playwright install chromium
```

Expected: Installs dependencies + downloads Chromium browser to local cache.

- [ ] **Step 6: Commit**

```bash
git add smoke/
git commit -m "feat: scaffold smoke/ directory with playwright config"
```

---

### Task 2: Write API client

**Files:**
- Create: `smoke/lib/api.ts`

The API client uses `page.request` (Playwright's `APIRequestContext` tied to the page's browser context) so the `Set-Cookie` from `create-user` is automatically carried into subsequent `page.goto()` calls.

- [ ] **Step 1: Create `smoke/lib/api.ts`**

```typescript
import type { APIRequestContext } from '@playwright/test';

interface SetupStatus {
  setupRequired: boolean;
  authentikReady: boolean;
}

interface CreateUserResponse {
  success: boolean;
  error?: string;
}

interface InstalledApp {
  name: string;
  status: 'running' | 'starting' | 'installing' | 'uninstalling' | 'stopped' | 'error';
}

export class SmokeApi {
  private readonly baseUrl = 'http://bloud.local';

  constructor(private readonly request: APIRequestContext) {}

  async waitForSetupReady(timeoutMs = 5 * 60 * 1000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      try {
        const response = await this.request.get(`${this.baseUrl}/api/setup/status`);
        if (response.ok()) {
          const data: SetupStatus = await response.json();
          if (data.authentikReady) return;
        }
      } catch {
        // not up yet
      }
      await sleep(5_000);
    }
    throw new Error('Timeout waiting for Authentik to be ready (5 minutes)');
  }

  async createUser(username: string, password: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/setup/create-user`, {
      data: { username, password },
    });
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to create user: ${response.status()} - ${text}`);
    }
    const data: CreateUserResponse = await response.json();
    if (!data.success) {
      throw new Error(`Failed to create user: ${data.error}`);
    }
  }

  async installApp(name: string): Promise<void> {
    const response = await this.request.post(`${this.baseUrl}/api/apps/${name}/install`, {
      data: {},
    });
    if (!response.ok()) {
      const text = await response.text();
      throw new Error(`Failed to install ${name}: ${response.status()} - ${text}`);
    }
  }

  async waitForAppRunning(name: string, timeoutMs = 5 * 60 * 1000): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < timeoutMs) {
      try {
        const response = await this.request.get(`${this.baseUrl}/api/apps/installed`);
        if (response.ok()) {
          const apps: InstalledApp[] = await response.json();
          const app = apps.find((a) => a.name === name);
          if (app?.status === 'running') return;
        }
      } catch {
        // not up yet
      }
      await sleep(10_000);
    }
    throw new Error(`Timeout waiting for ${name} to reach running state (5 minutes)`);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
```

- [ ] **Step 2: Commit**

```bash
git add smoke/lib/api.ts
git commit -m "feat: add smoke API client for setup and install flow"
```

---

### Task 3: Write smoke test

**Files:**
- Create: `smoke/tests/smoke.spec.ts`

Key design: `page.request` shares the browser context's cookie jar with `page`. The `Set-Cookie` header from `create-user` is stored in the context, so `page.goto('/apps/miniflux')` is automatically authenticated.

- [ ] **Step 1: Create `smoke/tests/smoke.spec.ts`**

```typescript
import { test, expect } from '@playwright/test';
import { SmokeApi } from '../lib/api';

const TEST_USERNAME = 'smoketest';
const TEST_PASSWORD = 'smoketest123';

test('setup wizard, miniflux install, and shell embed', async ({ page }) => {
  // Use page.request so the session cookie from create-user flows through to page.goto()
  const api = new SmokeApi(page.request);

  // 1. Wait for Authentik to be ready (up to 5 minutes after VM boot)
  await api.waitForSetupReady();

  // 2. Create initial admin user — response sets session cookie in this page context
  await api.createUser(TEST_USERNAME, TEST_PASSWORD);

  // 3. Trigger Miniflux install
  await api.installApp('miniflux');

  // 4. Wait for Miniflux to reach running state (up to 5 minutes)
  await api.waitForAppRunning('miniflux');

  // 5. Navigate to Miniflux in the Bloud shell — session cookie is present
  await page.goto('/apps/miniflux');

  // Wait for the iframe to be attached — Miniflux may have polling requests that
  // would cause waitForLoadState('networkidle') to hang indefinitely
  await page.waitForSelector('iframe', { state: 'attached' });

  // 6. Full-page screenshot of the Bloud shell with Miniflux embedded
  await expect(page).toHaveScreenshot('miniflux.png', { fullPage: true });
});
```

- [ ] **Step 2: Verify TypeScript compiles (no build errors)**

Run from `smoke/`:
```bash
cd smoke && npx tsc --noEmit
```

Expected: No errors.

- [ ] **Step 3: Commit**

```bash
git add smoke/tests/smoke.spec.ts
git commit -m "feat: add smoke test — setup wizard + miniflux install + visual regression"
```

---

## Chunk 2: CLI command + usage

### Task 4: Add `smoke` CLI command

**Files:**
- Modify: `cli/pve.go` — add `cmdSmokePVE`
- Modify: `cli/main.go` — add `smoke` case + update `printUsage`

- [ ] **Step 1: Add `cmdSmokePVE` to `cli/pve.go`**

Add after `cmdSnapshotPVE` (before the end of the file) at `cli/pve.go:1410`:

```go
// cmdSmokePVE builds a fresh ISO, deploys it to the Proxmox test VM, then runs
// the Playwright smoke suite in smoke/ against http://bloud.local.
//
// Always implies --build and --install: a fresh ISO is built and installed every run.
// VM is left running after completion for manual inspection.
//
// Flags:
//   --update-snapshots  Pass through to Playwright to refresh committed baseline images
func cmdSmokePVE(args []string) int {
	updateSnapshots := false
	for _, arg := range args {
		if arg == "--update-snapshots" {
			updateSnapshots = true
		}
	}

	// Build ISO + deploy VM + run installer + health checks
	if code := cmdStartPVE([]string{"--build", "--install"}); code != 0 {
		return code
	}

	root, err := getProjectRoot()
	if err != nil {
		errorf("Could not find project root: %v", err)
		return 1
	}

	smokeDir := filepath.Join(root, "smoke")

	// Install node_modules if not present
	if _, err := os.Stat(filepath.Join(smokeDir, "node_modules")); os.IsNotExist(err) {
		log("Installing smoke test dependencies...")
		c := exec.Command("npm", "ci")
		c.Dir = smokeDir
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
		if err := c.Run(); err != nil {
			errorf("Failed to install smoke test dependencies: %v", err)
			return 1
		}
	}

	// Run Playwright smoke tests
	log("Running smoke tests against http://bloud.local...")
	fmt.Println()

	playwrightArgs := []string{"playwright", "test"}
	if updateSnapshots {
		playwrightArgs = append(playwrightArgs, "--update-snapshots")
	}

	c := exec.Command("npx", playwrightArgs...)
	c.Dir = smokeDir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		fmt.Println()
		errorf("Smoke tests failed")
		fmt.Printf("  View report: cd smoke && npx playwright show-report\n")
		return 1
	}

	fmt.Println()
	log("Smoke tests passed")
	fmt.Printf("  VM is running. To tear down: ./bloud destroy\n")
	return 0
}
```

- [ ] **Step 2: Add `smoke` case to the switch in `cli/main.go`**

In `cli/main.go`, add after the `snapshot` case (around line 163):

```go
	case "smoke":
		if isPVEMode() {
			exitCode = cmdSmokePVE(args)
		} else {
			fmt.Fprintf(os.Stderr, "%sError:%s 'smoke' is only available in Proxmox mode (set BLOUD_PVE_HOST)\n", colorRed, colorReset)
			exitCode = 1
		}
```

- [ ] **Step 3: Add `smoke` to the Proxmox usage block in `printUsage`**

In `cli/main.go` in the `printUsage()` function, add after the `start` flags block (around line 211):

```go
	fmt.Println("Change validation:")
	fmt.Println("  smoke [--update-snapshots]  Build ISO + full install + Playwright visual regression")
	fmt.Println()
```

- [ ] **Step 4: Build CLI to verify no compile errors**

```bash
cd cli && go build ./...
```

Expected: No errors.

- [ ] **Step 5: Commit**

```bash
git add cli/main.go cli/pve.go
git commit -m "feat: add ./bloud smoke command for visual regression validation"
```

---

### Task 5: Create initial snapshot baseline

This task requires a running Proxmox environment and must be done manually after the other tasks are complete.

- [ ] **Step 1: Run smoke with `--update-snapshots` to generate the baseline**

```bash
./bloud smoke --update-snapshots
```

This builds the ISO, installs the VM, runs the Playwright test, and writes the baseline PNG to `smoke/tests/smoke.spec.ts-snapshots/miniflux-chromium-darwin.png` (filename includes OS + browser for portability).

- [ ] **Step 2: Inspect the baseline screenshot**

Open `smoke/tests/smoke.spec.ts-snapshots/` and verify the screenshot shows:
- The Bloud shell chrome (sidebar, nav) is visible
- The Miniflux app content is loaded inside the iframe (not a login page, not a Bloud-in-Bloud embed)
- The page looks correct

- [ ] **Step 3: Commit the baseline**

```bash
git add smoke/tests/smoke.spec.ts-snapshots/
git commit -m "chore: add initial smoke test baseline screenshot"
```

- [ ] **Step 4: Verify regression detection works**

Run without `--update-snapshots` to confirm the baseline comparison runs:

```bash
./bloud smoke
```

Expected: Tests pass (compares against baseline, matches).

---

## Notes

### Updating the baseline

When an intentional UI change causes the screenshot to differ, refresh the baseline:

```bash
./bloud smoke --update-snapshots
git add smoke/tests/smoke.spec.ts-snapshots/
git commit -m "chore: update smoke baseline after <describe change>"
```

### Debugging failures

On test failure, Playwright saves a diff to `smoke/test-results/`. To view the HTML report:
```bash
cd smoke && npx playwright show-report
```

The VM stays running after any `./bloud smoke` run (pass or fail), so you can SSH in to inspect:
```bash
./bloud shell
```
