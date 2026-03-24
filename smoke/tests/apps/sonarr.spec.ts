import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('sonarr', 'Sonarr', async ({ page, appFrame }) => {
  await test.step('Sonarr UI loads', async () => {
    // forward-auth passes the Authentik session through — no login prompt.
    await expect(appFrame.getByRole('link', { name: 'Series' })).toBeVisible({ timeout: 30_000 });
  });

  await test.step('screenshot Sonarr in shell', async () => {
    await expect(page.locator('iframe')).toHaveScreenshot('sonarr.png');
  });
});
