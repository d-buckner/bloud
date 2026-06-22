import { test, expect } from '../lib/fixtures';
import { TEST_CREDS } from './constants';

test.describe('jellyfin (LDAP)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('jellyfin');
  });

  test('LDAP login reaches Jellyfin dashboard', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open Jellyfin from the Bloud home screen
    await page.goto('/');
    await page.getByText('Jellyfin').click();
    const appFrame = page.frameLocator('iframe');

    // Wait for the Jellyfin login page inside the iframe
    await expect(appFrame.locator('#loginPage')).toBeVisible({ timeout: 60_000 });

    // LDAP users don't appear in the public user list — use manual login
    const manualLoginBtn = appFrame.locator('.btnManualLogin');
    if (await manualLoginBtn.isVisible({ timeout: 2_000 }).catch(() => false)) {
      await manualLoginBtn.click();
    }

    await appFrame.locator('#txtManualName').fill(TEST_CREDS.USERNAME);
    await appFrame.locator('#txtManualPassword').fill(TEST_CREDS.PASSWORD);
    await appFrame.getByRole('button', { name: 'Sign in' }).click();

    // Jellyfin dashboard should load
    await expect(appFrame.locator('#indexPage')).toBeVisible({ timeout: 30_000 });
  });
});
