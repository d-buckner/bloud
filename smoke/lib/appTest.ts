import { test, expect, type Page, type FrameLocator } from '@playwright/test';
import { LoginPage } from './login-page';

type AppTestFixtures = {
  page: Page;
  appFrame: FrameLocator;
};

type AppTestCallback = (fixtures: AppTestFixtures) => Promise<void>;

export function appTest(appName: string, displayName: string, callback: AppTestCallback): void {
  const title = displayName;

  test(appName, async ({ page }) => {
    const consoleLogs: string[] = [];

    page.on('console', (msg) => {
      const text = `[${msg.type()}] ${msg.text()}`;
      consoleLogs.push(text);
      process.stdout.write(`  PAGE LOG: ${text}\n`);
    });
    page.on('pageerror', (err) => {
      const text = `[pageerror] ${err.message}`;
      consoleLogs.push(text);
      process.stdout.write(`  PAGE ERROR: ${text}\n`);
    });

    await new LoginPage(page).ensureSignedIn();

    await test.step(`install ${title}`, async () => {
      await page.goto('/catalog');
      await page.getByRole('heading', { name: title }).click();

      const modal = page.locator('dialog');
      const getButton = modal.getByRole('button', { name: 'Get', exact: true });
      const alreadyInstalled = !(await getButton.isVisible({ timeout: 15_000 }).catch(() => false));
      if (!alreadyInstalled) {
        await getButton.click();
        await expect(page.getByText('Installed')).toBeVisible({ timeout: 5 * 60 * 1000 });
      }

      await page.goto('/');
    });

    const appFrame = await test.step(`open ${title}`, async () => {
      await page.getByText(title).click();
      return page.frameLocator('iframe');
    });

    await callback({ page, appFrame });

    await test.info().attach('console-logs', {
      contentType: 'text/plain',
      body: Buffer.from(consoleLogs.join('\n'), 'utf-8'),
    });
  });
}
