import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('prowlarr', 'Prowlarr', async ({ page, appFrame }) => {
  await test.step('Prowlarr UI loads', async () => {
    // forward-auth passes the Authentik session through — no login prompt.
    await expect(appFrame.getByRole('link', { name: 'Indexers' })).toBeVisible({ timeout: 30_000 });
  });

  await test.step('screenshot Prowlarr in shell', async () => {
    await expect(page.locator('iframe')).toHaveScreenshot('prowlarr.png');
  });
});
