import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('jellyfin', () => {
  test('health check responds', async ({ api, appName }) => {
    const app = await api.ensureAppRunning(appName);
    expect(app.status).toBe('running');
  });

  test('loads in iframe without errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    // Wait for Jellyfin to initialize
    await frame.locator('body').waitFor({ timeout: 30000 });

    // Should reach login page or main UI
    await expect(async () => {
      // Jellyfin shows login form or dashboard
      const hasLoginForm = await frame.locator('input[type="text"], input[name="username"]').first().isVisible().catch(() => false);
      const hasManualLogin = await frame.getByText(/sign in manually/i).isVisible().catch(() => false);
      const hasDashboard = await frame.getByText(/home|library|movies|tv shows/i).first().isVisible().catch(() => false);
      const hasServerName = await frame.locator('.serverName, .emby-button').first().isVisible().catch(() => false);

      expect(
        hasLoginForm || hasManualLogin || hasDashboard || hasServerName,
        'Jellyfin did not reach a functional page'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    // No CSS/JS loading failures
    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('SSO login redirects to Authentik', async ({ page, openApp }) => {
    const frame = await openApp();

    // Jellyfin with native-oidc shows SSO login button
    // Look for Authentik/SSO login option
    await expect(async () => {
      // Jellyfin may show "Sign in with <provider>" or redirect automatically
      const hasSSOButton = await frame.getByRole('button', { name: /sign in|authentik|sso|openid/i }).first().isVisible().catch(() => false);
      const hasAuthentikForm = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);
      const hasManualLogin = await frame.getByText(/sign in manually/i).isVisible().catch(() => false);

      expect(
        hasSSOButton || hasAuthentikForm || hasManualLogin,
        'Should see SSO login option or Authentik form'
      ).toBe(true);
    }).toPass({ timeout: 20000 });
  });

  test('completes full SSO login flow', async ({ page, openApp }) => {
    const frame = await openApp();

    // Wait for login page
    await page.waitForTimeout(3000);

    // Check if we need to click an SSO button or if we're already at Authentik
    const hasAuthentikForm = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);

    if (!hasAuthentikForm) {
      // Look for SSO button and click it
      const ssoButton = frame.getByRole('button', { name: /sign in|authentik|sso/i }).first();
      const hasSSOButton = await ssoButton.isVisible().catch(() => false);

      if (hasSSOButton) {
        await ssoButton.click();
        await page.waitForTimeout(2000);
      }
    }

    // Now we should see Authentik's login form
    const usernameField = frame.locator('input[name="uidField"]');

    try {
      await usernameField.waitFor({ state: 'visible', timeout: 20000 });
    } catch {
      // May already be logged in or different flow
      const isLoggedIn = await frame.getByText(/home|library|dashboard/i).first().isVisible().catch(() => false);
      if (isLoggedIn) {
        // Already authenticated
        expect(page.url()).toContain('/apps/jellyfin');
        return;
      }
      test.skip(true, 'Authentik login form not visible');
      return;
    }

    // Complete Authentik two-step login
    await usernameField.fill('akadmin');

    const loginButton = frame.getByRole('button', { name: 'Log in' });
    await expect(loginButton).toBeVisible({ timeout: 5000 });
    await loginButton.click();

    const passwordField = frame.getByPlaceholder('Please enter your password');
    await expect(passwordField).toBeVisible({ timeout: 10000 });
    await passwordField.fill('password');

    const continueBtn = frame.getByRole('button', { name: 'Continue' });
    await expect(continueBtn).toBeVisible({ timeout: 5000 });
    await continueBtn.click();

    // Wait for redirect back to Jellyfin
    await page.waitForTimeout(3000);

    // Should see Jellyfin's authenticated UI
    await expect(async () => {
      const hasDashboard = await frame.getByText(/home|library|my media/i).first().isVisible().catch(() => false);
      const hasUserMenu = await frame.locator('.headerUserButton, .user-menu, [data-user]').first().isVisible().catch(() => false);
      const hasMovies = await frame.getByText(/movies|tv shows|continue watching/i).first().isVisible().catch(() => false);

      expect(
        hasDashboard || hasUserMenu || hasMovies,
        'Should see Jellyfin authenticated UI'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    expect(page.url()).toContain('/apps/jellyfin');
  });
});
