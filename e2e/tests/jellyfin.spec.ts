// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';

// Jellyfin uses LDAP authentication. The LDAP admin filter
// (configurator.go:773) grants admin to members of "authentik Admins",
// which the bootstrap admin ("admin") is. The setup user (TEST_CREDS) is
// NOT in "authentik Admins", so LDAP login as the setup user fails.
// Log into Jellyfin as "admin" with the Authentik bootstrap password,
// matching the Go e2e test (e2e_test.go:730-733).
const JELLYFIN_LDAP_CREDS = {
  USERNAME: 'admin',
  PASSWORD: process.env.BLOUD_AUTHENTIK_ADMIN_PASSWORD ?? 'password',
} as const;

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

    await jellyfinPage.locator('#txtManualName').fill(JELLYFIN_LDAP_CREDS.USERNAME);
    await jellyfinPage.locator('#txtManualPassword').fill(JELLYFIN_LDAP_CREDS.PASSWORD);
    await jellyfinPage.getByRole('button', { name: 'Sign in' }).click();

    // Jellyfin dashboard should load
    await expect(jellyfinPage.locator('#indexPage')).toBeVisible({ timeout: 30_000 });
  });
});
