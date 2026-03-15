import { test, expect } from '@playwright/test';

// Navigate to Authentik login page without signing in first so we see the styled login form.
// This test exists purely to capture a screenshot as evidence that branding CSS is applied.
test('authentik login page styling', async ({ page }) => {
  await test.step('load Authentik login page', async () => {
    await page.goto('/if/flow/default-authentication-flow/');
    await page.locator('input[name="uidField"]').waitFor({ state: 'visible', timeout: 30_000 });
  });

  await test.step('screenshot login page', async () => {
    await expect(page).toHaveScreenshot('authentik-login.png', { fullPage: true });
  });
});
