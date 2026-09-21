// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Vaultwarden uses
// native-oidc with SSO_ONLY: the web vault's login page offers only "Use single
// sign-on".
//
// The browser sign-in itself is not covered here: the Bitwarden web client
// refuses any server whose URL is not https://, and Bloud serves apps over plain
// HTTP (apps/vaultwarden/INTEGRATION.md, "Plain HTTP"). The Go integration test
// covers the full SSO login against the server.
describeApp('vaultwarden', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: a single container on its own SQLite database. When
    // this fails, the UI rungs below are skipped, which distinguishes a broken
    // install from a misbehaving app.
    test.setTimeout(10 * 60_000);
    await ensureInstalled('vaultwarden');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Vaultwarden');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Vaultwarden');
  });

  test('the sign-in page offers single sign-on only', async () => {
    test.setTimeout(120_000);
    const vault = await openAppFromHome(app.page, 'Vaultwarden');
    try {
      await expect(vault).toHaveURL(/#\/login/, { timeout: 60_000 });
      await expect(vault.getByRole('button', { name: /single sign-on/i })).toBeVisible();

      // SSO_ONLY: the identity provider is the only way in. Master-password
      // login and self-registration are gone from the page.
      await expect(vault.getByRole('button', { name: 'Other' })).toHaveCount(0);
      await expect(vault.getByRole('link', { name: 'Create account' })).toHaveCount(0);
    } finally {
      await vault.close();
    }
  });
});
