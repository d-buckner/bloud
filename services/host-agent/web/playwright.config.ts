import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright configuration for Bloud integration tests.
 *
 * These tests run against the test environment (port 8081).
 * Prerequisites:
 *   1. ./bloud test start
 *   2. Install the app being tested (e.g., qbittorrent)
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: process.env.CI ? 1 : undefined,
  reporter: 'list',
  use: {
    // Test environment uses port 8081
    baseURL: process.env.BLOUD_TEST_URL || 'http://localhost:8081',
    trace: 'on-first-retry',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
});
