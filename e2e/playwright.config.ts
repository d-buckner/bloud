import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.BLOUD_URL ?? 'http://localhost:3000';

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
  timeout: 10 * 60_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.01,
    },
  },
  projects: [
    {
      name: 'jellyfin',
      testMatch: 'tests/apps/jellyfin.spec.ts',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'navidrome',
      testMatch: 'tests/apps/navidrome.spec.ts',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
  outputDir: 'test-results',
});
