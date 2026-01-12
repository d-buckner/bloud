import { test, expect } from '../lib/fixtures';

test.describe('Home Page', () => {
  test('loads successfully', async ({ trackedPage: page }) => {
    await page.goto('/');
    await expect(page).toHaveTitle(/bloud/i);
  });

  test('displays installed apps', async ({ trackedPage: page, api }) => {
    // Ensure at least one app is installed
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(2000);

    // Should show the app
    await expect(page.getByText('Miniflux')).toBeVisible({ timeout: 5000 });
  });
});

test.describe('App Launcher', () => {
  test('clicking app opens it in viewer', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(2000);

    // Click on the app
    await page.locator('.app-slot').first().click();

    // Should navigate to app viewer
    await expect(page).toHaveURL(/\/apps\//, { timeout: 5000 });

    // App iframe should be visible
    await expect(page.locator('iframe')).toBeVisible({ timeout: 10000 });
  });

  test('app iframe loads content', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/apps/miniflux/');

    const iframe = page.locator('iframe');
    await expect(iframe).toBeVisible({ timeout: 10000 });

    const src = await iframe.getAttribute('src');
    expect(src).toContain('/embed/miniflux');
  });
});

test.describe('Context Menu', () => {
  test('right-click shows context menu', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(2000);

    // Right-click on app
    await page.locator('.app-slot').first().click({ button: 'right' });

    // Context menu should appear
    await expect(page.locator('.context-menu')).toBeVisible({ timeout: 3000 });
    await expect(page.getByText('Remove App')).toBeVisible();
  });
});

// Run empty state test last - it uninstalls all apps
test.describe('Empty State', () => {
  test.skip('shows empty state when no apps installed', async ({ trackedPage: page, api }) => {
    // This test is skipped by default because it uninstalls all apps
    // Run it explicitly with: npx playwright test -g "shows empty state"
    const userApps = await api.getUserApps();
    for (const app of userApps) {
      await api.uninstallApp(app.name);
    }
    for (const app of userApps) {
      await api.waitForAppUninstalled(app.name);
    }

    await page.goto('/');
    await expect(page.getByText('No apps yet')).toBeVisible({ timeout: 10000 });
  });
});
