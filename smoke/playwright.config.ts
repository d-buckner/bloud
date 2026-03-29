import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.BLOUD_URL ?? 'http://bloud.local';

// When running against a Proxmox VM, mDNS (bloud.local) may not reach the test host
// across network boundaries (e.g. WiFi ↔ wired bridge). Inject a Chrome host resolver
// rule so the browser resolves bloud.local → VM IP without relying on mDNS.
const vmIP = process.env.BLOUD_VM_IP;
const chromeLaunchArgs = vmIP
  ? [`--host-resolver-rules=MAP bloud.local ${vmIP}`]
  : [];
const APPS = [
  'authentik',
  'miniflux',
  'jellyfin',
  'adguard-home',
  'qbittorrent',
];

function createAppEntries() {
  return APPS.map(name => ({
    name,
    testMatch: `tests/apps/${name}.spec.ts`,
    dependencies: ['setup'],
    use: { ...devices['Desktop Chrome'], launchOptions: { args: chromeLaunchArgs } },
  }));
}

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
    baseURL,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    actionTimeout: 10_000,
    navigationTimeout: 60_000,
  },
  // 20 minutes: installer (~5 min) + reboot + services (~3 min) + setup + apps (~5 min) + buffer
  timeout: 1_200_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
    },
  },
  projects: [
    {
      name: 'setup',
      testMatch: 'tests/setup.spec.ts',
      use: { ...devices['Desktop Chrome'], launchOptions: { args: chromeLaunchArgs } },
    },
    ...createAppEntries(),
    {
      name: 'teardown',
      testMatch: 'tests/auth.spec.ts',
      dependencies: APPS,
      use: { ...devices['Desktop Chrome'], launchOptions: { args: chromeLaunchArgs } },
    },
  ],
  outputDir: 'test-results',
});
