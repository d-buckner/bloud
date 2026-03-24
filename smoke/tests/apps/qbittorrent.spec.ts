import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('qbittorrent', 'qBittorrent', async ({ page, appFrame }) => {
  await test.step('Flood UI loads', async () => {
    // Flood auth is disabled (forward-auth handles it). The Authentik session
    // established during setup is passed through, so Flood loads without a login prompt.
    // Wait for the application root — Flood renders a .application container when ready.
    await expect(appFrame.locator('.application')).toBeVisible({ timeout: 30_000 });
  });

  await test.step('screenshot qBittorrent in shell', async () => {
    await expect(page.locator('iframe')).toHaveScreenshot('qbittorrent.png');
  });
});
