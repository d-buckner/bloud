import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.BLOUD_URL ?? 'http://bloud.local';

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
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'authentik',
      testMatch: 'tests/apps/authentik.spec.ts',
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'miniflux',
      testMatch: 'tests/apps/miniflux.spec.ts',
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'jellyfin',
      testMatch: 'tests/apps/jellyfin.spec.ts',
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'adguard-home',
      testMatch: 'tests/apps/adguard-home.spec.ts',
      dependencies: ['setup'],
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  outputDir: 'test-results',
});
