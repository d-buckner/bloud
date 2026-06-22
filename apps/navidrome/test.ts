import { test, expect } from '../../integration/lib/app-test';

test.describe('navidrome', () => {
  test('health check responds', async ({ api, appName }) => {
    const app = await api.ensureAppRunning(appName);
    expect(app.status).toBe('running');
  });

  test('SSO login flow completes and reaches Navidrome', async ({ page, appName }) => {
    // Navigate directly to Navidrome (not via iframe — it's a full-page app with forward-auth)
    await page.goto(`http://navidrome.localhost:8080/`);

    // Should be redirected to Authentik login
    await expect(page).toHaveURL(/localhost:8080\/application\/o\/authorize/, { timeout: 15000 });

    // Fill in Authentik login form (identifier-first flow)
    const usernameField = page.getByRole('textbox', { name: /email or username/i });
    await expect(usernameField).toBeVisible({ timeout: 15000 });
    await usernameField.fill('admin');

    await page.getByRole('button', { name: /log in/i }).click();

    const passwordField = page.getByRole('textbox', { name: /password/i });
    await expect(passwordField).toBeVisible({ timeout: 10000 });
    await passwordField.fill('password');

    await page.getByRole('button', { name: /log in/i }).click();

    // OAuth callback should complete and land on Navidrome
    await expect(page).toHaveURL(/navidrome\.localhost:8080/, { timeout: 20000 });
    await expect(page).not.toHaveURL(/outpost\.goauthentik\.io\/callback/, { timeout: 5000 });

    // Navidrome UI should load — wait for the app shell
    await expect(
      page.locator('#root, .nd-app, nav, [data-testid="player"]').first()
    ).toBeVisible({ timeout: 20000 });
  });
});
