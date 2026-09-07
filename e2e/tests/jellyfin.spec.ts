// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile, openAppFromHome } from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { TEST_CREDS } from './constants';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Rungs go pre-auth →
// authed so no case depends on app state that a later case consumes.
describeApp('jellyfin', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs below are skipped, which distinguishes
    // a broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('jellyfin');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Jellyfin');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Jellyfin');
  });

  test('opens from the home tile and serves its login page', async () => {
    test.setTimeout(180_000);
    const jellyfin = await openAppFromHome(app.page, 'Jellyfin');
    try {
      // The popup is really Jellyfin (not an error page or a stuck proxy),
      // and the app is up and answering: LDAP users authenticate against
      // Bloud's directory, so Jellyfin serves its login page directly.
      await expect(jellyfin).toHaveURL(/jellyfin\.localhost:8080/, {
        timeout: 30_000,
      });
      await expect(jellyfin.locator('#loginPage')).toBeVisible({
        timeout: 60_000,
      });
    } finally {
      await jellyfin.close();
    }
  });

  test('signs in with LDAP credentials and reaches the dashboard', async () => {
    test.setTimeout(300_000);
    const jellyfin = await openAppFromHome(app.page, 'Jellyfin');

    // LDAP users don't appear in the public user list — use manual login.
    const manualLoginBtn = jellyfin.locator('.btnManualLogin');
    if (
      await manualLoginBtn.isVisible({ timeout: 5_000 }).catch(() => false)
    ) {
      await manualLoginBtn.click();
    }

    await jellyfin.locator('#txtManualName').fill(TEST_CREDS.USERNAME);
    await jellyfin.locator('#txtManualPassword').fill(TEST_CREDS.PASSWORD);
    await jellyfin.getByRole('button', { name: 'Sign in' }).click();

    // Jellyfin dashboard should load.
    await expect(jellyfin.locator('#indexPage')).toBeVisible({
      timeout: 60_000,
    });
  });
});
