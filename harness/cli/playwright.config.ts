// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, devices } from '@playwright/test';

/**
 * The CLI recipe runner.
 *
 * One config for the ad-hoc recipes, separate from the e2e config: the e2e
 * suite runs the full behavior ladders and keeps traces and video, an agent
 * recipe runs one page observation and prints what it saw. `bin/browser.mjs`
 * launches this config and passes the recipe in through the environment.
 */
export default defineConfig({
  testDir: '.',
  testMatch: 'recipe.spec.ts',
  fullyParallel: false,
  workers: 1,
  reporter: [['list']],
  timeout: 5 * 60_000,
  use: {
    ...devices['Desktop Chrome'],
    // A recipe reports what it found through stdout, so the traces that the
    // e2e suite keeps would only slow the run down.
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
  outputDir: '.recipe-output',
});
