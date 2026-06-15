import { test, expect } from '@playwright/test';
import { SmokeApi } from '../lib/api';
import { LoginPage } from '../lib/login-page';
import { TEST_CREDS } from './constants';


test('installer', async ({ page }) => {
  const installButton = page.getByRole('button', { name: 'Install Bloud' });

  await test.step('load installer', async () => {
    // Retry navigation — bloud.local mDNS may lag a few seconds after ISO boot
    await expect(() => page.goto('/')).toPass({ timeout: 60_000, intervals: [5_000] });

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
});

test('setup', async ({ page }) => {
  const api = new SmokeApi(page.request);
  const loginPage = new LoginPage(page);

  await test.step('wait for services', async () => {
    await api.waitForSetupReady();
  });

  await test.step('complete setup wizard', async () => {
    // Retry navigation — browser DNS for bloud.local may lag behind Node.js mDNS resolution
    await expect(() => page.goto('/')).toPass({ timeout: 60_000, intervals: [5_000] });
    await page.getByLabel('Username').fill(TEST_CREDS.USERNAME);
    await page.getByLabel('Password', { exact: true }).fill(TEST_CREDS.PASSWORD);
    await page.getByLabel('Confirm Password').fill(TEST_CREDS.PASSWORD);
    await page.getByRole('button', { name: 'Create Account' }).click();
  });

  await test.step('authenticate with Authentik', async () => {
    // After submit, the page reloads with no session — redirects to /auth/login which
    // starts the OIDC flow, establishing both the Bloud and Authentik sessions.
    // The Authentik session is what allows app OIDC flows (e.g. Miniflux) to auto-approve.
    await loginPage.login();
    await page.waitForURL('/', { timeout: 30_000 });
  });
});
