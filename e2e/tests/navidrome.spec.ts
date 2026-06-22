import { test, expect } from '../lib/fixtures';
import { TEST_CREDS } from './constants';

const TRAEFIK_BASE = 'http://navidrome.localhost:8080';

test.describe('navidrome (forward-auth)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('navidrome');
  });

  test('SSO login reaches Navidrome UI', async ({ page }) => {
    // Navigate to Navidrome through Traefik — forward-auth redirects to Authentik
    await page.goto(TRAEFIK_BASE);

    // Wait for Authentik login page (forward-auth redirect)
    const usernameField = page.locator('input[name="uidField"]');
    await expect(usernameField).toBeVisible({ timeout: 30_000 });

    // Fill in Authentik identifier-first login
    await usernameField.click();
    await usernameField.pressSequentially(TEST_CREDS.USERNAME);
    await page.getByRole('button', { name: 'Log in' }).click();

    const passwordField = page.locator('input[name="password"]');
    await expect(passwordField).toBeVisible({ timeout: 10_000 });
    await passwordField.click();
    await page.waitForTimeout(1_000);
    await passwordField.pressSequentially(TEST_CREDS.PASSWORD);
    await page.getByRole('button', { name: 'Continue' }).click();

    // After OAuth callback, should land back on Navidrome
    await expect(page).toHaveURL(/navidrome\.localhost:8080/, { timeout: 30_000 });

    // Navidrome UI should render — look for the app shell
    await expect(
      page.locator('#root, .MuiBox-root, nav').first(),
    ).toBeVisible({ timeout: 30_000 });
  });
});
