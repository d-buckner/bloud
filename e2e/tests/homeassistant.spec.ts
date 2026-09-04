// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';

const HA_URL = 'http://homeassistant.localhost:8080';

test.describe('homeassistant (native-oidc)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('homeassistant');
  });

  test('SSO login reaches the Home Assistant dashboard', async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    // Open Home Assistant from the Bloud home screen — opens in a new tab.
    await page.goto('/');
    const haPromise = page.waitForEvent('popup');
    await page.getByText('Home Assistant').click();
    const ha = await haPromise;
    await ha.waitForLoadState();
    // HA's provider-selection page ("Continue with Bloud") renders inside the
    // popup, not the home-screen tab.
    const bloudButton = ha.getByRole('button', { name: /Bloud/ });

    // A fresh HA install has no owner yet, so the browser holds no Home
    // Assistant session: the visit must be redirected out to the IdP.
    // hass-oidc-auth is the default provider (features.default_redirect), so
    // no "Continue with Bloud" click should be needed — but click it if the
    // provider-selection page ever shows, to keep the journey robust.
    const haLoginPage = new LoginPage(ha);
    const deadline = Date.now() + 240_000;
    for (;;) {
      const url = ha.url();
      if (url.startsWith(HA_URL) && !url.includes('/auth/')) break;
      if (Date.now() > deadline) break;
      if (await haLoginPage.isVisible()) {
        await haLoginPage.login();
        continue;
      }
      if (await bloudButton.isVisible({ timeout: 1_000 }).catch(() => false)) {
        await bloudButton.click();
        continue;
      }
      await ha.waitForTimeout(500);
    }

    // Must land back on Home Assistant outside its auth flow — not on the
    // Authentik flow page and not on HA's provider-selection (/auth/authorize).
    await expect(ha).toHaveURL(/^http:\/\/homeassistant\.localhost:8080\/(?!auth\/)/, {
      timeout: 60_000,
    });

    // The Lovelace dashboard shell renders only for an authenticated Home
    // Assistant session; an unauthenticated browser sits on the auth flow.
    await expect(
      ha.locator('ha-panel-lovelace, lovelace-ui').first(),
    ).toBeVisible({ timeout: 60_000 });

    // The sidebar shows the signed-in identity (display name claim from the
    // IdP), proving the account came from Authentik rather than HA's built-in
    // legacy provider.
    await expect(ha.locator('sidebar-user')).toBeVisible({ timeout: 60_000 });

    // No leftover credentials form on the dashboard view.
    await expect(ha.locator('input[name="uidField"]')).toHaveCount(0);
  });
});
