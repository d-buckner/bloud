import { test, expect } from '@playwright/test';
import { SmokeApi } from '../lib/api';
import { LoginPage } from '../lib/login-page';

const TEST_USERNAME = 'smoketest';
const TEST_PASSWORD = 'smoketest123';

test('installer, setup wizard, miniflux install, and shell embed', async ({ page }) => {
  const api = new SmokeApi(page.request);
  const loginPage = new LoginPage(page);

  // ── Installer ────────────────────────────────────────────────────────────────

  const installButton = page.getByRole('button', { name: 'Install Bloud' });

  await test.step('load installer', async () => {
    await page.goto('/');

    // Wait for hardware detection — retry if the disk API fails on first load after boot
    await expect(async () => {
      const retryBtn = page.getByRole('button', { name: 'Retry' });
      if (await retryBtn.isVisible({ timeout: 0 })) {
        await retryBtn.click();
      }
      await expect(installButton).toBeEnabled();
    }).toPass({ timeout: 60_000, intervals: [3_000] });

    await expect(page).toHaveScreenshot('installer-welcome.png', { fullPage: true });
  });

  await test.step('run installer', async () => {
    await installButton.click();

    // Wait for installation to complete — "Your server is restarting." indicates success
    await page.getByText('Your server is restarting.').waitFor({
      state: 'visible',
      timeout: 10 * 60 * 1000,
    });

    await expect(page).toHaveScreenshot('installer-restarting.png', { fullPage: true });
  });

  // ── Setup wizard ─────────────────────────────────────────────────────────────

  await test.step('wait for services', async () => {
    await api.waitForSetupReady();
  });

  await test.step('complete setup wizard', async () => {
    await page.goto('/');
    await page.getByLabel('Username').fill(TEST_USERNAME);
    await page.getByLabel('Password', { exact: true }).fill(TEST_PASSWORD);
    await page.getByLabel('Confirm Password').fill(TEST_PASSWORD);
    await page.getByRole('button', { name: 'Create Account' }).click();
  });

  await test.step('authenticate with Authentik', async () => {
    // After submit, the page reloads with no session — redirects to /auth/login which
    // starts the OIDC flow, establishing both the Bloud and Authentik sessions.
    // The Authentik session is what allows app OIDC flows (e.g. Miniflux) to auto-approve.
    await loginPage.login(TEST_USERNAME, TEST_PASSWORD);
    await page.waitForURL('/', { timeout: 30_000 });
  });

  // ── App install ──────────────────────────────────────────────────────────────

  await test.step('install Miniflux', async () => {
    await page.goto('/catalog');
    await page.getByRole('heading', { name: 'Miniflux' }).click();

    // Scoped to dialog to avoid matching app-card buttons behind the modal overlay
    const modal = page.locator('dialog');
    const getButton = modal.getByRole('button', { name: 'Get', exact: true });
    await expect(getButton).toBeVisible({ timeout: 15_000 });
    await getButton.click();

    await expect(page.getByText('Installed')).toBeVisible({
      timeout: 5 * 60 * 1000,
    });

    await page.goto('/');
  });

  // ── Visual regression ────────────────────────────────────────────────────────

  await test.step('screenshot Miniflux in shell', async () => {
    // sso_launch_path redirects the iframe directly to the OIDC endpoint so
    // Authentik auto-approves and Miniflux loads logged in — no login prompt.
    await page.getByText('Miniflux').click();

    const miniflux = page.frameLocator('iframe');
    await expect(miniflux.getByRole('link', { name: 'Unread' })).toBeVisible({ timeout: 30_000 });
    await expect(page).toHaveScreenshot('miniflux.png', { fullPage: true });
  });
});
