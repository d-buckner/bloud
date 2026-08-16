import { test, expect } from '../lib/fixtures';
import { LoginPage } from '../lib/loginPage';

test.describe('immich (native-oidc)', () => {
  test.beforeEach(async ({ api }) => {
    await api.ensureInstalled('immich');
  });

  test('SSO login reaches the Immich app', async ({ authenticatedPage }) => {
    const page = authenticatedPage;

    // Open Immich from the Bloud home screen — opens in a new tab
    await page.goto('/');
    const immichPagePromise = page.waitForEvent('popup');
    await page.getByText('Immich').click();
    const immichPage = await immichPagePromise;
    await immichPage.waitForLoadState();

    // With SSO enabled, Immich auto-launches the OIDC flow from its login
    // page. The flow round-trips through Authentik on the issuer host
    // (sso.localhost); complete the login page there if the browser session
    // did not carry over. First-time SSO users are created on login and
    // walk Immich's onboarding wizard before reaching the photos page.
    //
    // Cold starts (fresh install: DB migrations + geodata import + first
    // OIDC round-trip) can take a while, so allow a generous deadline; the
    // 10-minute test timeout remains the outer bound.
    const loginPage = new LoginPage(immichPage);
    const nextButton = immichPage.locator('#onboarding-card button').last();
    const deadline = Date.now() + 300_000;
    for (;;) {
      const url = immichPage.url();
      if (url.includes('/photos')) break;
      if (Date.now() > deadline) break;

      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }

      // Onboarding wizard: click through each step until it completes.
      if (await nextButton.isVisible({ timeout: 1_000 }).catch(() => false)) {
        await nextButton.click();
        await immichPage.waitForTimeout(500);
        continue;
      }

      await immichPage.waitForTimeout(500);
    }

    // The OIDC callback lands back on Immich, which exchanges the code and
    // redirects authenticated users to the photos page.
    await expect(immichPage).toHaveURL(/immich\.localhost:8080\/photos/, {
      timeout: 60_000,
    });
    // The Immich app shell should render — the top navigation bar (id
    // dashboard-navbar) is present only for authenticated users. The mobile
    // "Go to search" icon is hidden at desktop widths, so assert the shell.
    await expect(immichPage.locator('nav#dashboard-navbar')).toBeVisible({
      timeout: 30_000,
    });
  });
});
