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
      const needsInstall = await getButton.waitFor({ state: 'visible', timeout: 15_000 })
        .then(() => true)
        .catch(() => false);
      if (needsInstall) {
        await getButton.click();
        // Wait for the catalog card (not the modal) to show the "Installed" badge.
        // The modal may close on its own after install; the card badge is stable.
        const card = page.getByRole('button', { name: title });
        await expect(card.getByText('Installed')).toBeVisible({ timeout: 7 * 60 * 1000 });
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
