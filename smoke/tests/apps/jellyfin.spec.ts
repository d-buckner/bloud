import { expect, test } from '@playwright/test';
import { appTest } from '../../lib/appTest';
import { TEST_CREDS } from '../constants';

appTest('jellyfin', 'Jellyfin', async ({ appFrame }) => {
  await test.step('log in to Jellyfin', async () => {
    await expect(appFrame.locator('#loginPage')).toBeVisible({ timeout: 60_000 });

    // LDAP users don't appear in the public user list — use the manual login form
    const manualLoginBtn = appFrame.locator('.btnManualLogin');
    if (await manualLoginBtn.isVisible({ timeout: 2_000 }).catch(() => false)) {
      await manualLoginBtn.click();
    }

    await appFrame.locator('#txtManualName').fill(TEST_CREDS.USERNAME);
    await appFrame.locator('#txtManualPassword').fill(TEST_CREDS.PASSWORD);
    await appFrame.getByRole('button', { name: 'Sign in' }).click();

    await expect(appFrame.locator('#indexPage')).toBeVisible({ timeout: 30_000 });
  });
});
