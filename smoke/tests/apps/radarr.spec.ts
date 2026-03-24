import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('radarr', 'Radarr', async ({ page, appFrame }) => {
  await test.step('Radarr UI loads', async () => {
    // forward-auth passes the Authentik session through — no login prompt.
    await expect(appFrame.getByRole('link', { name: 'Movies' })).toBeVisible({ timeout: 30_000 });
  });

  await test.step('screenshot Radarr in shell', async () => {
    await expect(page.locator('iframe')).toHaveScreenshot('radarr.png');
  });
});
