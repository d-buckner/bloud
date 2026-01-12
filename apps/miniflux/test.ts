import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('miniflux', () => {
  test('loads in iframe without errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    // Should show login form, SSO link, or feed content
    await expect(
      frame.locator('form, .items, .page-header, a[href]').first()
    ).toBeVisible({ timeout: 10000 });

    // No CSS/JS loading failures
    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('health check responds', async ({ api, appName, embedPath, request }) => {
    await api.ensureAppRunning(appName);

    const response = await request.get(`${embedPath}healthcheck`);
    expect(response.ok()).toBe(true);
  });

  test('SSO login flow works', async ({ openApp, page }) => {
    const frame = await openApp();

    // Click "Sign in with Bloud SSO"
    const ssoLink = frame.getByRole('link', { name: /Sign in with Bloud SSO/i });
    await expect(ssoLink).toBeVisible({ timeout: 10000 });
    await ssoLink.click();

    // Wait for Authentik login page to load in iframe
    await page.waitForTimeout(1000);

    // Username field - Authentik uses "Email or Username" label
    const usernameField = frame.getByRole('textbox', { name: /email or username/i });
    await expect(usernameField).toBeVisible({ timeout: 15000 });
    await usernameField.fill('akadmin');

    // Click "Log in" to go to password step (identifier-first flow)
    const loginBtn = frame.getByRole('button', { name: /log in/i });
    await expect(loginBtn).toBeVisible({ timeout: 5000 });
    await loginBtn.click();

    // Wait for password step to load
    await page.waitForTimeout(500);

    // Password field - Authentik uses a textbox with label "Password"
    const passwordField = frame.getByRole('textbox', { name: /password/i });
    await expect(passwordField).toBeVisible({ timeout: 10000 });
    await passwordField.fill('password');

    // Submit login - button might be "Log in" or "Continue"
    const submitBtn = frame.getByRole('button', { name: /log in|continue/i });
    await expect(submitBtn).toBeVisible({ timeout: 5000 });
    await submitBtn.click();

    // Wait for OAuth flow to complete and redirect back to miniflux
    await page.waitForTimeout(3000);

    // Verify we're now logged into miniflux - should see feeds/unread/settings
    // Re-get the frame after potential navigation
    const newFrame = page.frameLocator('iframe');
    await expect(
      newFrame.locator('a[href*="unread"], a[href*="feeds"], .page-header, .item').first()
    ).toBeVisible({ timeout: 15000 });
  });
});
