import { test, expect } from '@playwright/test';
import { SmokeApi } from '../lib/api';

const TEST_USERNAME = 'smoketest';
const TEST_PASSWORD = 'smoketest123';

test('setup wizard, miniflux install, and shell embed', async ({ page }) => {
  // Use page.request so the session cookie from create-user flows through to page.goto()
  const api = new SmokeApi(page.request);

  // 1. Wait for Authentik to be ready (up to 5 minutes after VM boot)
  await api.waitForSetupReady();

  // 2. Create initial admin user — response sets session cookie in this page context
  await api.createUser(TEST_USERNAME, TEST_PASSWORD);

  // 3. Trigger Miniflux install
  await api.installApp('miniflux');

  // 4. Wait for Miniflux to reach running state (up to 5 minutes)
  await api.waitForAppRunning('miniflux');

  // 5. Navigate to Miniflux in the Bloud shell — session cookie is present
  await page.goto('/apps/miniflux');

  // Wait for the iframe to be attached — Miniflux may have polling requests that
  // would cause waitForLoadState('networkidle') to hang indefinitely
  await page.waitForSelector('iframe', { state: 'attached' });

  // 6. Full-page screenshot of the Bloud shell with Miniflux embedded
  await expect(page).toHaveScreenshot('miniflux.png', { fullPage: true });
});
