import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('miniflux', 'Miniflux', async ({ page, appFrame }) => {
  await test.step('screenshot Miniflux in shell', async () => {
    // sso_launch_path redirects the iframe directly to the OIDC endpoint so
    // Authentik auto-approves and Miniflux loads logged in — no login prompt.
    await expect(appFrame.getByRole('link', { name: 'Unread' })).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('iframe')).toHaveScreenshot('miniflux.png');
  });
});
