import { test, expect } from '@playwright/test';
import { LoginPage } from '../lib/login-page';

const LOGIN_PATH = '/if/flow';

test('logout clears both Bloud and Authentik sessions', async ({ page }) => {
  const loginPage = new LoginPage(page);

  await test.step('ensure signed in', async () => {
    await loginPage.ensureSignedIn();
  });

  await test.step('click logout', async () => {
    await page.getByRole('button', { name: 'Sign out' }).click();
    // Logout → /auth/logout → Authentik end_session → bloud.local/ → /auth/login → /if/flow
    await page.waitForURL(url => url.href.includes(LOGIN_PATH), { timeout: 30_000 });
  });

  await test.step('verify session is cleared', async () => {
    // Navigating to / must redirect to login — both Bloud and Authentik sessions are gone
    await page.goto('/');
    await page.waitForURL(url => url.href.includes(LOGIN_PATH), { timeout: 15_000 });
  });
});
