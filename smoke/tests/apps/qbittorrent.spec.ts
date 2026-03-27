import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('qbittorrent', 'qBittorrent', async ({ page, appFrame }) => {
  await test.step('qBittorrent UI loads', async () => {
    // forward-auth passes the Authentik session through — no login prompt.
    // linuxserver/qbittorrent renders a header with the app name when ready.
    await expect(appFrame.locator('#header')).toBeVisible({ timeout: 30_000 });
  });

  await test.step('screenshot qBittorrent in shell', async () => {
    await expect(page.locator('iframe')).toHaveScreenshot('qbittorrent.png');
  });
});
