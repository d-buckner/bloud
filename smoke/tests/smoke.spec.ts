import { test, expect } from '@playwright/test';
import { SmokeApi } from '../lib/api';

const TEST_USERNAME = 'smoketest';
const TEST_PASSWORD = 'smoketest123';

test('installer, setup wizard, miniflux install, and shell embed', async ({ page }) => {
  // Use page.request so the session cookie from create-user flows through to page.goto()
  const api = new SmokeApi(page.request);

  // ── Installer ────────────────────────────────────────────────────────────────

  // 1. Navigate to installer (live ISO serves it at bloud.local)
  await page.goto('/');

  // Wait for hardware detection to complete — button is disabled until disk is auto-selected
  const installButton = page.getByRole('button', { name: 'Install Bloud' });
  await expect(installButton).toBeEnabled({ timeout: 60_000 });

  // Screenshot: installer welcome page
  await expect(page).toHaveScreenshot('installer-welcome.png', { fullPage: true });

  // 2. Click Install with defaults (auto-selected disk, encryption enabled)
  await installButton.click();

  // Wait for installation to complete — "Your server is restarting." indicates success
  await page.getByText('Your server is restarting.').waitFor({
    state: 'visible',
    timeout: 10 * 60 * 1000,
  });

  // Screenshot: post-install restarting page
  await expect(page).toHaveScreenshot('installer-restarting.png', { fullPage: true });

  // ── Post-install setup ───────────────────────────────────────────────────────

  // 3. Wait for Authentik to be ready on the installed system
  // (Restarting.svelte polls /api/health and auto-redirects when installed system is up)
  await api.waitForSetupReady();

  // 4. Create initial admin user — response sets session cookie in this page context
  await api.createUser(TEST_USERNAME, TEST_PASSWORD);

  // ── App install ──────────────────────────────────────────────────────────────

  // 5. Navigate to the app catalog and install Miniflux via the UI
  await page.goto('/catalog');

  // Click the Miniflux card to open the detail modal
  await page.getByRole('heading', { name: 'Miniflux' }).click();

  // Click the "Get" button to trigger installation
  await page.getByRole('button', { name: 'Get' }).click();

  // Wait for installation to complete — modal shows "This app is installed" when running (up to 5 min)
  await expect(page.getByText('This app is installed')).toBeVisible({
    timeout: 5 * 60 * 1000,
  });

  // Close the modal to return to the catalog
  await page.getByRole('button', { name: 'Close' }).click();

  // ── Visual regression ────────────────────────────────────────────────────────

  // 7. Navigate to Miniflux in the Bloud shell — session cookie is present
  await page.goto('/apps/miniflux');

  // Wait for the iframe to be attached — Miniflux may have polling requests that
  // would cause waitForLoadState('networkidle') to hang indefinitely
  await page.waitForSelector('iframe', { state: 'attached' });

  // Screenshot: Bloud shell with Miniflux embedded
  await expect(page).toHaveScreenshot('miniflux.png', { fullPage: true });
});
