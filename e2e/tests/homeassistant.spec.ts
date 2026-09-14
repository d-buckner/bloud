// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
      // provider welcome screen ("Login with Bloud") — either is proof
      // the gate held. An unauthenticated browser can never reach the
      // Lovelace shell, so failing to render it while an auth screen
      // renders is the assertion.
      const loginPage = new LoginPage(ha);
      const bloudLink = ha.getByRole('link', { name: /Bloud/ });
      const deadline = Date.now() + 90_000;
      for (;;) {
        if (await loginPage.isVisible()) break;
        if (await bloudLink.first().isVisible({ timeout: 1_000 }).catch(() => false)) break;
        if (Date.now() > deadline) {
          throw new Error(
            `popup reached neither an auth screen nor the dashboard (url: ${ha.url()})`,
          );
        }
        await ha.waitForTimeout(500);
      }
      expect(ha.url()).toMatch(/homeassistant\.localhost:8080|sso\.localhost/);
    } finally {
      await ha.close();
    }
  });

  test('signs in through OIDC and reaches the dashboard', async () => {
    test.setTimeout(360_000);
    const ha = await openAppFromHome(app.page, 'Home Assistant');

    // Gate screens each verified against the live instance; they render
    // inside open shadow roots, which Playwright CSS locators pierce:
    //   • the Authentik identifier-first login (no IdP session carried over)
    //   • hass-oidc-auth welcome screen → "Login with Bloud" (an <a>, not a
    //     button: /auth/oidc/redirect)
    //   • the provider's "Logged in!" page → "Continue on this device"
    //   • HA first-run onboarding wizard (core config → analytics → finish);
    //     repeat runs skip it
    // The terminal state is the authenticated Lovelace shell — an
    // unauthenticated browser can never render it.
    const dashboard = ha.locator('ha-panel-lovelace, lovelace-ui');
    const bloudLink = ha.getByRole('link', { name: /Bloud/ });
    const continueBtn = ha.getByRole('button', { name: /Continue on this device/ });
    const wizardButton = ha.getByRole('button', { name: /^(Next|Finish|Skip)$/ });
    const loginPage = new LoginPage(ha);

    const deadline = Date.now() + 300_000;
    for (;;) {
      if (await dashboard.first().isVisible({ timeout: 1_000 }).catch(() => false)) {
        break;
      }
      if (Date.now() > deadline) break;
      if (await loginPage.isVisible().catch(() => false)) {
        await loginPage.login();
        continue;
      }
      if (await bloudLink.first().isVisible({ timeout: 1_000 }).catch(() => false)) {
        await bloudLink.first().click();
        continue;
      }
      if (await continueBtn.first().isVisible({ timeout: 1_000 }).catch(() => false)) {
        await continueBtn.first().click();
        continue;
      }
      if (await wizardButton.first().isVisible({ timeout: 1_000 }).catch(() => false)) {
        await wizardButton.first().click().catch(() => {});
        continue;
      }
      await ha.waitForTimeout(500);
    }

    // Must land back on Home Assistant outside its auth flow and outside
    // the first-run wizard.
    await expect(ha).toHaveURL(/^http:\/\/homeassistant\.localhost:8080\/(?!auth\/)/, {
      timeout: 60_000,
    });
    expect(ha.url()).not.toMatch(/onboarding/);

    // The sidebar shows the signed-in identity (display-name claim from
    // the IdP), proving the account came from Authentik rather than HA's
    // built-in legacy provider.
    await expect(ha.locator('ha-user-badge')).toBeVisible({ timeout: 60_000 });
  });
});
