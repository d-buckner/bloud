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

    // Forward-auth may redirect through Authentik; log in if needed
    const loginPage = new LoginPage(navidromePage);
    await loginPage.loginIfNeeded();

    // Should land on Navidrome
    await expect(navidromePage).toHaveURL(/navidrome\.localhost:8080/, { timeout: 30_000 });

    // Navidrome UI should render — look for the app shell
    await expect(
      navidromePage.locator('#root, .MuiBox-root, nav').first(),
    ).toBeVisible({ timeout: 30_000 });
  });
});
