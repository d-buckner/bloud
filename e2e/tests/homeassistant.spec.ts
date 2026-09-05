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

    // Between the popup and the dashboard sit several gates (each verified
    // against the live instance; all render inside open shadow roots, which
    // Playwright CSS locators pierce):
    //   • Authentik identifier-first login (only without an IdP session)
    //   • hass-oidc-auth welcome screen → "Login with Bloud" (an <a>, not a
    //     button: /auth/oidc/redirect)
    //   • the provider's "Logged in!" page → "Continue on this device"
    //   • HA first-run onboarding wizard (core config → analytics → finish)
    // The terminal state is the authenticated Lovelace shell: HA 2026.x uses
    // a "home-assistant" custom-element shell whose sidebar shows the signed-in
    // identity as `ha-user-badge`. An unauthenticated browser sits on an
    // /auth/ flow page, /onboarding.html, or the provider welcome screen.
    const dashboard = ha.locator('ha-panel-lovelace, lovelace-ui');
    const bloudLink = ha.getByRole('link', { name: /Bloud/ });
    const continueBtn = ha.getByRole('button', { name: /Continue on this device/ });
    const wizardButton = ha.getByRole('button', { name: /^(Next|Finish|Next|Finish|Skip)$/ });
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

    // Must land back on Home Assistant outside its auth flow and outside the
    // first-run wizard.
    await expect(ha).toHaveURL(/^http:\/\/homeassistant\.localhost:8080\/(?!auth\/)/, {
      timeout: 60_000,
    });
    expect(ha.url()).not.toMatch(/onboarding/);

    // The sidebar shows the signed-in identity (display-name claim from the
    // IdP), proving the account came from Authentik rather than HA's built-in
    // legacy provider.
    await expect(ha.locator('ha-user-badge').first()).toBeVisible({
      timeout: 60_000,
    });

    // No leftover credentials form on the dashboard view.
    await expect(ha.locator('input[name="uidField"]')).toHaveCount(0);
  });
});
