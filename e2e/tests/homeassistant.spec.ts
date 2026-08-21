// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';

test.describe('homeassistant (native-oidc)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('homeassistant');
  });

  test('SSO login reaches Home Assistant UI', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open Home Assistant from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const homeassistantPagePromise = page.waitForEvent('popup');
    await page.getByText('Home Assistant').click();
    const homeassistantPage = await homeassistantPagePromise;
    await homeassistantPage.waitForLoadState();
    // native-oidc may redirect through Authentik; log in if needed.
    const loginPage = new LoginPage(homeassistantPage);
    const deadline = Date.now() + 120_000;
    for (;;) {
      const url = homeassistantPage.url();
      if (url.includes('homeassistant.localhost:8080') && !url.includes('/auth/login')) break;
      if (Date.now() > deadline) break;

      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }

      await homeassistantPage.waitForTimeout(500);
    }

    // Should land on Home Assistant
    await expect(homeassistantPage).toHaveURL(/homeassistant\.localhost:8080/, { timeout: 30_000 });

    // Home Assistant UI should render — look for the app shell
    await expect(
      homeassistantPage.locator('#root, .MuiBox-root, nav').first(),
    ).toBeVisible({ timeout: 30_000 });
  });
});