import type { Page } from '@playwright/test';
import { TEST_CREDS } from '../tests/constants';

/**
 * Perform the Authentik identifier-first login flow on the given page.
 * Assumes the page is already on the Authentik login screen.
 */
async function login(page: Page): Promise<void> {
  const usernameField = page.locator('input[name="uidField"]');
  await usernameField.waitFor({ state: 'visible', timeout: 90_000 });
  await usernameField.click();
  await usernameField.pressSequentially(TEST_CREDS.USERNAME);
  await page.getByRole('button', { name: 'Log in' }).click();

  const passwordField = page.locator('input[name="password"]');
  await passwordField.waitFor({ state: 'visible', timeout: 10_000 });
  await passwordField.click();
  await page.waitForTimeout(1_000);
  await passwordField.pressSequentially(TEST_CREDS.PASSWORD);
  await page.getByRole('button', { name: 'Continue' }).click();
}

function isAuthURL(url: URL): boolean {
  return url.pathname.startsWith('/if/flow') || url.pathname === '/auth/login';
}

/**
 * Navigate to / and sign in via Authentik if needed.
 * No-ops if the session is already authenticated.
 * Throws if the setup wizard is showing (VM not provisioned).
 */
export async function ensureSignedIn(page: Page): Promise<void> {
  await page.goto('/');

  // SvelteKit loads / first, checks auth via JS, then redirects through
  // /auth/login → Authentik /if/flow/... when unauthenticated. The full
  // chain involves two async fetches + a server redirect, so allow 15s.
  const isAuthPage = await page
    .waitForURL((url) => isAuthURL(url), { timeout: 15_000 })
    .then(() => true)
    .catch(() => false);

  // Fail fast if the setup wizard is showing — the VM hasn't been configured.
  const isSetupWizard = await page
    .getByRole('heading', { name: 'Welcome to Bloud' })
    .isVisible();
  if (isSetupWizard) {
    throw new Error(
      'Portable runtime is unconfigured: setup wizard is showing. ' +
        'Provision the E2E environment before running Playwright.',
    );
  }

  if (!isAuthPage) return;

  // If we landed on /auth/login (server redirect to Authentik), wait for
  // the final Authentik login page to load.
  if (!page.url().includes('/if/flow')) {
    await page.waitForURL((url) => url.pathname.startsWith('/if/flow'), {
      timeout: 15_000,
    });
  }

  await login(page);
  await page.waitForURL('/', { timeout: 30_000 });
}
