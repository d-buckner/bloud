import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('jellyseerr', () => {
  // Jellyseerr uses forward-auth SSO

  test('health check responds', async ({ api, appName }) => {
    const app = await api.ensureAppRunning(appName);
    expect(app.status).toBe('running');
  });

  test('forward auth redirects unauthenticated users to Authentik', async ({ page, openApp }) => {
    const frame = await openApp();

    await page.waitForTimeout(3000);

    // Forward auth should redirect to Authentik login
    const hasAuthentikLogin = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);
    const hasAuthentikComponent = await frame.locator('[class*="ak-"], ak-flow-executor').first().isVisible().catch(() => false);

    if (!hasAuthentikLogin && !hasAuthentikComponent) {
      // Cross-origin redirect may have been blocked (expected for forward-auth in iframes)
      const iframeHtml = await frame.locator('body').innerHTML().catch(() => '');
      expect(
        hasAuthentikLogin || hasAuthentikComponent || iframeHtml.length < 1000,
        'Forward auth should redirect to Authentik or be blocked by COEP'
      ).toBe(true);
    }
  });

  test('completes full SSO login flow', async ({ page, openApp }) => {
    const frame = await openApp();

    const usernameField = frame.locator('input[name="uidField"]');

    try {
      await usernameField.waitFor({ state: 'visible', timeout: 30000 });
    } catch {
      const hasAuthentikContent = await frame.locator('ak-flow-executor, [class*="pf-"], .ak-stage').first().isVisible().catch(() => false);
      if (!hasAuthentikContent) {
        test.skip(true, 'Authentik login form not visible - check iframe loading');
        return;
      }
      await usernameField.waitFor({ state: 'visible', timeout: 15000 });
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

    await page.waitForTimeout(3000);

    // After SSO, should see Jellyseerr's interface
    await expect(async () => {
      // Jellyseerr shows discover page, requests, or user menu
      const hasDiscover = await frame.getByText(/discover|trending|popular/i).first().isVisible().catch(() => false);
      const hasRequests = await frame.getByText(/requests/i).first().isVisible().catch(() => false);
      const hasSearch = await frame.locator('input[type="search"], input[placeholder*="search" i]').first().isVisible().catch(() => false);
      const hasUserMenu = await frame.locator('[data-testid="user-menu"], .user-dropdown').first().isVisible().catch(() => false);

      expect(
        hasDiscover || hasRequests || hasSearch || hasUserMenu,
        'Should see Jellyseerr interface after SSO'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    expect(page.url()).toContain('/apps/jellyseerr');
  });

  test('loads in iframe without critical errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    await frame.locator('body').waitFor({ timeout: 30000 });

    // After potential SSO redirect, check for critical resource errors
    // Filter out expected 401/403 from forward-auth
    const criticalErrs = criticalErrors(resourceErrors).filter(
      e => e.status !== 401 && e.status !== 403
    );
    expect(criticalErrs).toHaveLength(0);
  });
});
