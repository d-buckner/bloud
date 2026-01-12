import { test, expect, criticalErrors } from '../../integration/lib/app-test';

test.describe('actual-budget', () => {
  test('loads in iframe without errors', async ({ openApp, resourceErrors }) => {
    const frame = await openApp();

    // Wait for app to initialize
    await frame.locator('body').waitFor({ timeout: 30000 });

    // Check for SharedArrayBuffer error (requires COOP/COEP headers)
    const hasSharedArrayBufferError = await frame
      .getByText('SharedArrayBuffer')
      .isVisible()
      .catch(() => false);

    if (hasSharedArrayBufferError) {
      throw new Error(
        'SharedArrayBuffer not available. Server needs COOP/COEP headers.'
      );
    }

    // Check for Fatal Error dialog
    const hasFatalError = await frame
      .getByText('Fatal Error')
      .isVisible()
      .catch(() => false);

    if (hasFatalError) {
      throw new Error('App crashed with Fatal Error dialog.');
    }

    // Should reach login, setup, or budget UI
    // Use retry logic since app may still be initializing
    await expect(async () => {
      const hasLogin = await frame.getByText('Sign in').first().isVisible().catch(() => false);
      const hasServerConfig = await frame.getByText('No server configured').isVisible().catch(() => false);
      const hasOpenID = await frame.getByText(/openid/i).first().isVisible().catch(() => false);
      const hasBudgetUI = await frame.getByRole('button').first().isVisible().catch(() => false);

      expect(
        hasLogin || hasServerConfig || hasOpenID || hasBudgetUI,
        'App did not reach a functional page'
      ).toBe(true);
    }).toPass({ timeout: 15000 });

    // No CSS/JS loading failures
    expect(criticalErrors(resourceErrors)).toHaveLength(0);
  });

  test('health check responds', async ({ api, appName, embedPath, request }) => {
    await api.ensureAppRunning(appName);

    const response = await request.get(embedPath);
    expect(response.ok()).toBe(true);
  });

  test('SSO login loads Authentik in iframe', async ({ page, openApp }) => {
    const frame = await openApp();

    // Wait for Actual Budget's login page with OpenID button
    // Button text varies: "Start using OpenID" (fresh) or "Sign in with OpenID" (bootstrapped)
    const ssoButton = frame.getByRole('button', { name: /openid/i });
    await expect(ssoButton).toBeVisible({ timeout: 30000 });

    // Click the SSO button
    await ssoButton.click();

    // Verify Authentik login page loads IN THE IFRAME
    // Check for Authentik-specific elements (ak- prefix is Authentik's web component naming)
    await expect(async () => {
      // Authentik uses custom elements with ak- prefix and specific form fields
      const hasAuthentikUsername = await frame.locator('input[name="uidField"]').isVisible().catch(() => false);
      const hasAuthentikComponent = await frame.locator('[class*="ak-"], ak-flow-executor').first().isVisible().catch(() => false);

      expect(
        hasAuthentikUsername || hasAuthentikComponent,
        'Authentik login form should load inside iframe (looking for uidField input or ak- components)'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    // Double-check: the username field should accept input (proves it's a real form, not just styled text)
    const usernameField = frame.locator('input[name="uidField"]');
    await expect(usernameField).toBeVisible({ timeout: 5000 });
    await usernameField.fill('test-user');
    await expect(usernameField).toHaveValue('test-user');

    // Verify parent page URL hasn't changed (iframe contained the redirect)
    expect(page.url()).toContain('/apps/actual-budget');
  });

  test('completes full SSO login flow', async ({ page, openApp }) => {
    const frame = await openApp();

    // Step 1: Verify we're on Actual Budget's login page
    // Button text varies: "Start using OpenID" (fresh) or "Sign in with OpenID" (bootstrapped)
    const ssoButton = frame.getByRole('button', { name: /openid/i });
    await expect(ssoButton).toBeVisible({ timeout: 30000 });

    // Capture that we see Actual Budget's login UI before SSO
    // Heading is always "Sign in to this Actual instance"
    const hasActualBudgetLoginUI = await frame.getByRole('heading', { name: /sign in to this actual instance/i }).isVisible().catch(() => false);
    expect(hasActualBudgetLoginUI, 'Should see Actual Budget login page before SSO').toBe(true);

    // Step 2: Click SSO and verify Authentik appears
    await ssoButton.click();

    // Wait for Authentik's username field (specific to Authentik's form)
    const usernameField = frame.locator('input[name="uidField"]');
    await expect(usernameField).toBeVisible({ timeout: 20000 });

    // Step 3: Enter credentials in Authentik
    // Authentik has a two-step flow: username -> "Log in" -> password -> "Continue"
    await usernameField.fill('akadmin');

    // Click "Log in" to proceed to password step
    const loginButton = frame.getByRole('button', { name: 'Log in' });
    await expect(loginButton).toBeVisible({ timeout: 5000 });
    await loginButton.click();

    // Wait for password field to appear
    // Authentik's password field has placeholder "Please enter your password"
    const passwordField = frame.getByPlaceholder('Please enter your password');
    await expect(passwordField).toBeVisible({ timeout: 10000 });

    // Click to focus, then fill, then verify
    await passwordField.click();
    await passwordField.fill('password');
    await expect(passwordField).toHaveValue('password');

    // Click "Continue" to submit password
    const continueBtn = frame.getByRole('button', { name: 'Continue' });
    await expect(continueBtn).toBeVisible({ timeout: 5000 });
    await continueBtn.click();

    // Wait for navigation to complete after login
    await page.waitForTimeout(2000);

    // Step 4: Verify redirect back to Actual Budget in authenticated state
    // After successful OAuth, Actual Budget shows budget files list or main UI
    await expect(async () => {
      // Look for Actual Budget's authenticated state indicators:
      // - "logged in as:" text with username
      // - "Files" heading or budget file list
      // - "Server online" status
      // - "Create new file" or "Import file" buttons
      const hasLoggedInAs = await frame.getByText(/logged in as:/i).isVisible().catch(() => false);
      const hasFilesUI = await frame.getByText(/files/i).isVisible().catch(() => false);
      const hasServerOnline = await frame.getByText(/server online/i).isVisible().catch(() => false);
      const hasCreateFile = await frame.getByRole('button', { name: /create new file/i }).isVisible().catch(() => false);

      // Importantly: we should NOT see the login page anymore
      const stillOnLoginPage = await frame.getByRole('heading', { name: /sign in to this actual instance/i }).isVisible().catch(() => false);

      expect(stillOnLoginPage, 'Should no longer be on login page').toBe(false);
      expect(
        hasLoggedInAs || hasFilesUI || hasServerOnline || hasCreateFile,
        'Should see Actual Budget authenticated UI (logged in status, files, or create button)'
      ).toBe(true);
    }).toPass({ timeout: 20000 });

    // Verify parent page is still the app viewer
    expect(page.url()).toContain('/apps/actual-budget');
  });
});
