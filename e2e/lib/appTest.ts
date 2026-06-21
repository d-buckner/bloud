import { test, expect, type Page, type FrameLocator } from '@playwright/test';
import { LoginPage } from './login-page';

const EMBED_BASE = process.env.BLOUD_URL ?? 'http://localhost:3000';

type AppTestFixtures = {
  page: Page;
  appFrame: FrameLocator;
};

type AppTestCallback = (fixtures: AppTestFixtures) => Promise<void>;

export function appTest(appName: string, displayName: string, callback: AppTestCallback): void {
  const title = displayName;

  test(appName, async ({ page }) => {
    const consoleLogs: string[] = [];

    page.on('console', (msg) => consoleLogs.push(`[${msg.type()}] ${msg.text()}`));
    page.on('pageerror', (err) => consoleLogs.push(`[pageerror] ${err.message}`));

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

    await test.step(`check ${title} embed endpoint`, async () => {
      // Verify the embed endpoint is reachable before opening the iframe.
      // Don't follow redirects — auth redirects (302) mean the app is running.
      // Retry on 4xx (app not configured yet) and 5xx (app still starting).
      await expect(async () => {
        const response = await page.request.get(`${EMBED_BASE}/embed/${appName}/`, {
          maxRedirects: 0,
        });
        const status = response.status();
        if (status >= 400) {
          throw new Error(
            `Embed endpoint /embed/${appName}/ returned ${status} — app is not running`,
          );
        }
      }).toPass({ timeout: 2 * 60 * 1000, intervals: [5_000] });
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
