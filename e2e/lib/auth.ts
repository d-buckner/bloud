import type { Page } from '@playwright/test';
import { LoginPage } from './loginPage';

const LOGIN_PATH = '/if/flow';

/**
 * Navigate to / and sign in via Authentik if needed.
 * No-ops if the session is already authenticated.
 * Throws if the setup wizard is showing (VM not provisioned).
 *
 * All browser access goes through Traefik (port 8080), which routes
 * /auth/login and /if/flow/* to Authentik on the same origin.
 */
export async function ensureSignedIn(page: Page): Promise<void> {
  await page.goto('/');

  // SvelteKit loads at /, checks auth, then redirects through
  // /auth/login → /if/flow/... (all on the same Traefik origin).
  const isAuthPage = await page
    .waitForURL((url) => url.pathname.startsWith(LOGIN_PATH), {
      timeout: 15_000,
    })
    .then(() => true)
    .catch(() => false);

  // Fail fast if the setup wizard is showing — the VM hasn't been configured.
  const isSetupWizard = await page
    .getByRole('heading', { name: 'Welcome to Bloud' })
    .isVisible();
  if (isSetupWizard) {
    throw new Error(
      'Bloud is unconfigured: setup wizard is showing. ' +
        'Provision the E2E environment before running Playwright.',
    );
  }

  if (!isAuthPage) return;

  const loginPage = new LoginPage(page);
  await loginPage.login();
  await page.waitForURL('/', { timeout: 30_000 });
}
