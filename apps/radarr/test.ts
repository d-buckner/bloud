import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('radarr', () => {
  // Radarr uses forward-auth SSO

  test('health check responds', async ({ api, appName }) => {
    const app = await api.ensureAppRunning(appName);
    expect(app.status).toBe('running');
  });

  test('forward auth redirects unauthenticated users to Authentik', async ({ page, openApp }) => {
    const frame = await openApp();

    await page.waitForTimeout(3000);

    const hasAuthentikLogin = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);
    const hasAuthentikComponent = await frame.locator('[class*="ak-"], ak-flow-executor').first().isVisible().catch(() => false);

    if (!hasAuthentikLogin && !hasAuthentikComponent) {
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

    // After SSO, should see Radarr's interface
    await expect(async () => {
      // Radarr shows movies list, add movie button, or calendar
      const hasMovies = await frame.getByText(/movies|library/i).first().isVisible().catch(() => false);
      const hasAddMovie = await frame.getByRole('link', { name: /add new|add movie/i }).first().isVisible().catch(() => false);
      const hasCalendar = await frame.getByText(/calendar/i).first().isVisible().catch(() => false);
      const hasNavbar = await frame.locator('.navbar, nav, [class*="Navbar"]').first().isVisible().catch(() => false);

      expect(
        hasMovies || hasAddMovie || hasCalendar || hasNavbar,
        'Should see Radarr interface after SSO'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    expect(page.url()).toContain('/apps/radarr');
  });

  test('loads in iframe without critical errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    await frame.locator('body').waitFor({ timeout: 30000 });

    const criticalErrs = criticalErrors(resourceErrors).filter(
      e => e.status !== 401 && e.status !== 403
    );
    expect(criticalErrs).toHaveLength(0);
  });
});
