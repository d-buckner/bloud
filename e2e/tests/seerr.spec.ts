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
// means the first failure skips the rungs behind it.
//
// Seerr has no SSO to assert: it supports media-server and local email
// credentials only, so `sso.strategy` is `none` and there is no
// forward-auth at the ingress; the popup lands directly on Seerr's own
// UI. The observable rung is therefore which UI it lands on: a logged-in
// Seerr that has been onboarded shows its *login* page, while an instance
// that Bloud's PostStart never finished onboarding redirects every path
// to the first-run setup wizard instead. Asserting the login page (and no
// wizard) is the proof that the configurator's onboarding (Jellyfin
// connection created via `POST /api/v1/auth/jellyfin`, then
// `POST /api/v1/settings/initialize`) actually completed.
describeApp('seerr', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs below are skipped, which distinguishes
    // a broken install from a misbehaving app.
    //
    // Jellyfin is installed first because Seerr's onboarding needs a media
    // server: the configurator connects the two and completes Seerr's first-run
    // wizard, and the last rung below asserts exactly that state. Nothing else
    // installs it (the full suite only passes because jellyfin.spec.ts runs
    // first), so a per-app leg has to install its prerequisite itself.
    test.setTimeout(20 * 60_000);
    await ensureInstalled('jellyfin');
    await ensureInstalled('seerr');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Seerr');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Seerr');
  });

  test('serves the login UI, not the first-run setup wizard', async () => {
    test.setTimeout(180_000);
    const seerr = await openAppFromHome(app.page, 'Seerr');
    try {
      // With `initialized: true` a logged-out request is redirected to
      // Seerr's login page; with `initialized: false` every path is
      // redirected to /setup instead. Landing on /login is therefore the
      // observable evidence that Bloud's onboarding ran to completion.
      await expect(seerr).toHaveURL(/seerr\.localhost:8080\/login/, {
        timeout: 60_000,
      });

      // The login form is Seerr's own, with the Jellyfin sign-in
      // affordance Bloud's onboarding configured the server for.
      await expect(seerr.locator('form[data-form-type="login"]')).toBeVisible({
        timeout: 30_000,
      });
      await expect(
        seerr
          .locator('[data-testid="mediaserver-login-button"]')
          .or(seerr.getByText(/Login with Jellyfin/i))
          .first(),
      ).toBeVisible({ timeout: 30_000 });

      // Negative half of the rung: the first-run wizard's heading is
      // absent. A Seerr whose onboarding never completed redirects to
      // /setup and renders "Welcome to Seerr" instead of this page, so
      // such an instance fails here.
      await expect(seerr.getByText('Welcome to Seerr')).toHaveCount(0);
    } finally {
      await seerr.close();
    }
  });

});
