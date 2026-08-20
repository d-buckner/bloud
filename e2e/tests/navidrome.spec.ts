// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';

test.describe('navidrome (forward-auth)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('navidrome');
  });

  test('SSO login reaches Navidrome UI', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open Navidrome from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const navidromePagePromise = page.waitForEvent('popup');
    await page.getByText('Navidrome').click();
    const navidromePage = await navidromePagePromise;
    await navidromePage.waitForLoadState();
    // Forward-auth may redirect through Authentik; log in if needed. The
    // flow page is a React app that renders its form after document load,
    // so poll for the form within a deadline instead of checking once.
    const loginPage = new LoginPage(navidromePage);
    const deadline = Date.now() + 120_000;
    for (;;) {
      const url = navidromePage.url();
      if (url.includes('navidrome.localhost:8080') && !url.includes('/if/flow')) break;
      if (Date.now() > deadline) break;

      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }

      await navidromePage.waitForTimeout(500);
    }

    // Should land on Navidrome
    await expect(navidromePage).toHaveURL(/navidrome\.localhost:8080/, { timeout: 30_000 });

    // Navidrome UI should render — look for the app shell
    await expect(
      navidromePage.locator('#root, .MuiBox-root, nav').first(),
    ).toBeVisible({ timeout: 30_000 });
  });
});
