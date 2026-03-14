import { expect } from '@playwright/test';
import { test } from '@playwright/test';
import { appTest } from '../../lib/appTest';

appTest('adguard-home', 'AdGuard Home', async ({ page, appFrame }) => {
  await test.step('screenshot AdGuard Home in shell', async () => {
    await expect(appFrame.locator('span.counters__title').first()).toBeVisible({ timeout: 30_000 });
    await expect(page.locator('iframe')).toHaveScreenshot('adguard-home.png');
  });
});
