import { test, expect } from '@playwright/test';

test('authentik login page styling', async ({ page }) => {
  await test.step('load Authentik login page', async () => {
    await page.goto('/if/flow/default-authentication-flow/');
    await page.locator('input[name="uidField"]').waitFor({ state: 'visible', timeout: 30_000 });
  });

  await test.step('shows "Sign in to Bloud" title', async () => {
    // PostStart applies config asynchronously via systemd ExecStartPost. The Authentik SPA
    // fetches flow data once on page load and doesn't poll, so reload until the title is set.
    await expect(async () => {
      await page.reload();
      await page.locator('input[name="uidField"]').waitFor({ state: 'visible', timeout: 30_000 });
      await expect(page.getByRole('heading', { name: 'Sign in to Bloud', level: 1 })).toBeVisible();
    }).toPass({ timeout: 120_000, intervals: [5_000] });
  });

  await test.step('shows "Username" field (not "Email or Username")', async () => {
    // The identification stage should be configured for username-only.
    // Authentik renders the label as a generic element adjacent to the input.
    await expect(page.getByText('Email or Username')).not.toBeVisible();
    await expect(page.getByText('Username', { exact: true })).toBeVisible();
    await expect(page.locator('input[name="uidField"]')).toHaveAttribute('placeholder', 'Username');
  });

  await test.step('screenshot login page', async () => {
    // CSS branding is applied by PostStart (ExecStartPost), which runs asynchronously
    // after the container is healthy. The blueprint sets the title/username field before
    // PostStart runs, so we must explicitly wait for the CSS to be applied too.
    // Reload until the screenshot matches the baseline (off-white background, no forest image).
    await expect(async () => {
      await page.reload();
      await page.locator('input[name="uidField"]').waitFor({ state: 'visible', timeout: 30_000 });
      await expect(page).toHaveScreenshot('authentik-login.png', { fullPage: true });
    }).toPass({ timeout: 120_000, intervals: [5_000] });
  });
});
