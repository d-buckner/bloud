// SPDX-License-Identifier: AGPL-3.0-only
import { defineConfig, devices } from '@playwright/test';

const baseURL = process.env.BLOUD_URL ?? 'http://localhost:8080';

// The JSON report is how a retry stays visible: the e2e workflows read it and
// name every test that only passed on its second attempt. See `retries`.
const jsonReport = process.env.BLOUD_E2E_JSON_REPORT ?? 'test-results/e2e-report.json';

// One automatic re-run of a failed test in CI, none locally.
//
// The flakes in this suite are convergence timing (an app behind Traefik and
// Authentik not answering yet), not assertion bugs, so a single retry tells a
// transient miss apart from a real regression: a genuine failure fails on both
// attempts.
//
// This is the retry docs/plans/ci-flakiness-reduction.md guardrail 2 agreed
// to, and it arrived with a condition: "if a retry is added, report it, so it
// cannot quietly become the norm." The JSON reporter above plus the "Report
// retried tests" step in the e2e workflows satisfy that. A retried pass emits
// a workflow warning naming the spec; it does not fail the build, because
// failing on it is what the retry exists to avoid.
//
// The local default stays 0 on purpose: while a human is watching, a flake
// should surface as a flake. BLOUD_E2E_RETRIES overrides either way.
const retries = Number(process.env.BLOUD_E2E_RETRIES ?? (process.env.CI ? 1 : 0));

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries,
  workers: 1,
  reporter: [['html', { open: 'never' }], ['list'], ['json', { outputFile: jsonReport }]],
  use: {
    baseURL,
    ...devices['Desktop Chrome'],
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    actionTimeout: 10_000,
    navigationTimeout: 60_000,
  },
  timeout: 15 * 60_000,
  expect: {
    timeout: 10_000,
  },
  outputDir: 'test-results',
});
