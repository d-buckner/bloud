import { test, expect } from '../lib/fixtures';
import { TEST_CREDS } from './constants';

test.describe('jellyfin (LDAP)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('jellyfin');
  });

  test('LDAP login reaches Jellyfin dashboard', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open Jellyfin from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const jellyfinPagePromise = page.waitForEvent('popup');
    await page.getByText('Jellyfin').click();
    const jellyfinPage = await jellyfinPagePromise;

    // Wait for the Jellyfin login page in the new tab
    await jellyfinPage.waitForLoadState();
    await expect(jellyfinPage.locator('#loginPage')).toBeVisible({ timeout: 60_000 });

    // LDAP users don't appear in the public user list — use manual login
    const manualLoginBtn = jellyfinPage.locator('.btnManualLogin');
    if (await manualLoginBtn.isVisible({ timeout: 2_000 }).catch(() => false)) {
      await manualLoginBtn.click();
    }

    await jellyfinPage.locator('#txtManualName').fill(TEST_CREDS.USERNAME);
    await jellyfinPage.locator('#txtManualPassword').fill(TEST_CREDS.PASSWORD);
    await jellyfinPage.getByRole('button', { name: 'Sign in' }).click();

    // Jellyfin dashboard should load
    await expect(jellyfinPage.locator('#indexPage')).toBeVisible({ timeout: 30_000 });
  });
});
