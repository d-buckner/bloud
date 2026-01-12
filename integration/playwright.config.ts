import { defineConfig, devices } from '@playwright/test';

/**
 * Integration tests run in an isolated test VM with dedicated ports.
 *
 * Test ports (different from dev to allow parallel execution):
 * - BASE_URL: http://localhost:8081 (Traefik - test port)
 * - API_URL: http://localhost:3001 (Go API - test port)
 * - VITE_URL: http://localhost:5174 (Vite - test port)
 *
 * Environment variables:
 * - KEEP_TEST_VM=true - Keep test VM running after tests (for debugging)
 *
 * Examples:
 *   npm test                    # Create fresh test VM, run tests, destroy VM
 *   KEEP_TEST_VM=true npm test  # Keep test VM after tests for inspection
 */
export default defineConfig({
  testDir: '..',
  testMatch: ['integration/tests/**/*.spec.ts', 'apps/**/test.ts'],
  fullyParallel: false, // Run tests sequentially - app state matters
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1, // Single worker to maintain app state consistency
  reporter: [
    ['html', { open: 'never' }],
    ['list'],
  ],

  // Global setup and teardown for VM management
  globalSetup: './lib/global-setup.ts',
  globalTeardown: './lib/global-teardown.ts',

  use: {
    // Use Traefik on TEST port - it handles all routing (UI, API, embedded apps)
    baseURL: process.env.BASE_URL || 'http://localhost:8081',

    // Capture everything for debugging
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'on-first-retry',

    // Extended timeouts for app installations
    actionTimeout: 30_000,
    navigationTimeout: 30_000,
  },

  // Test timeout - 60 seconds max per test
  timeout: 60_000,

  // Expect timeout - 5 seconds for assertions
  expect: {
    timeout: 5_000,
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  // Output directories
  outputDir: 'test-results',
});
