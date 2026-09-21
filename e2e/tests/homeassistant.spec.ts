// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Home Assistant uses
// native-oidc via the hass-oidc-auth custom integration: the auth gate
// lives inside the app (the popup round-trips through the IdP and the
// provider's own welcome/consent screens), so the observable gate is "the
// popup shows an auth screen instead of the dashboard".
describeApp('homeassistant', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull (plus the hass-oidc-auth
    // custom integration download from GitHub) + first-run convergence.
    // When this fails, the UI rungs below are skipped, which distinguishes
    // a broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('homeassistant');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Home Assistant');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Home Assistant');
  });

  test('SSO gates the app: the popup lands on an auth screen', async () => {
    test.setTimeout(120_000);
    const ha = await openAppFromHome(app.page, 'Home Assistant');
    try {
      // A fresh popup has no Home Assistant session. Between the popup
      // and the dashboard sit the Authentik identifier-first login (when
      // the IdP session did not carry over) and the hass-oidc-auth
      // provider welcome screen ("Login with Bloud"); either is proof
      // the gate held. An unauthenticated browser can never reach the
      // Lovelace shell, so failing to render it while an auth screen
      // renders is the assertion.
      const loginPage = new LoginPage(ha);
      // Prefer the exact entry point over a name match: the provider's own
      // "Default login" link also sits on that screen, and it is the wrong
      // route (it skips Bloud entirely).
      const bloudLink = ha.locator('a[href="/auth/oidc/redirect"]');
      const authScreen = bloudLink.or(loginPage.usernameField);
      await expect(authScreen).toBeVisible({ timeout: 60_000 });
      // The gate held: no Lovelace view is rendered behind the auth screen.
      await expect(ha.locator('hui-view')).toHaveCount(0);
      expect(ha.url()).toMatch(/homeassistant\.localhost:8080|sso\.localhost/);
    } finally {
      await ha.close();
    }
  });

  test('signs in through OIDC and reaches the dashboard', async () => {
    // Measured against a live instance: the whole flow is ~20 s. The previous
    // 6-minute budget existed because this test polled `ha-panel-lovelace,
    // lovelace-ui` as its "we are in" marker, and neither element exists on the
    // current Overview dashboard, so the loop could only ever exit through its
    // deadline. Keep the budget tight enough that a future selector rot fails
    // fast instead of hiding as slowness.
    test.setTimeout(120_000);
    const ha = await openAppFromHome(app.page, 'Home Assistant');

    // Screens this flow can stop at, each verified against the live instance:
    //   • hass-oidc-auth welcome → <a href="/auth/oidc/redirect"> "Login with Bloud"
    //   • the Authentik identifier-first login (when the IdP session did not
    //     carry over); LoginPage's selectors resolve inside the nested open
    //     shadow roots Authentik renders
    //   • Home Assistant's own "Logged in!" page → "Continue on this device"
    //   • the authenticated shell → the Lovelace view (<hui-view>)
    //
    // Home Assistant completed first-run onboarding during convergence (the
    // app's PostStart does it), so no onboarding wizard appears here; if one
    // ever does, this test should fail rather than quietly click through it.
    const bloudLink = ha.locator('a[href="/auth/oidc/redirect"]');
    const continueBtn = ha.getByRole('button', { name: /Continue on this device/ });
    const view = ha.locator('hui-view');
    const loginPage = new LoginPage(ha);

    // `or()` resolves on whichever gate appears first, so each step waits on
    // something real instead of probing five selectors on fixed timeouts.
    const gate = bloudLink.or(loginPage.usernameField).or(continueBtn).or(view);

    for (let step = 0; step < 6; step++) {
      await expect(gate).toBeVisible({ timeout: 30_000 });
      if (await view.isVisible().catch(() => false)) break;
      if (await bloudLink.isVisible().catch(() => false)) {
        await bloudLink.click();
      } else if (await loginPage.isVisible().catch(() => false)) {
        await loginPage.login();
      } else if (await continueBtn.isVisible().catch(() => false)) {
        await continueBtn.click();
      } else {
        // `or()` reported something visible and none of the branches matched:
        // a new screen the selectors do not know about.
        throw new Error(`unknown screen in the login flow (url: ${ha.url()})`);
      }
    }

    // Terminal state: the authenticated dashboard. <hui-view> is the stable
    // marker: it survives Home Assistant renaming the panel element around it.
    await expect(view).toBeVisible({ timeout: 60_000 });
    expect(ha.url()).not.toMatch(/onboarding/);
    await expect(ha).toHaveURL(/^http:\/\/homeassistant\.localhost:8080\/(?!auth\/)/, {
      timeout: 30_000,
    });

    // The sidebar shows the signed-in identity (display-name claim from the
    // IdP), proving the account came from Authentik rather than HA's built-in
    // legacy provider.
    await expect(ha.locator('ha-user-badge')).toBeVisible({ timeout: 30_000 });
  });
});
