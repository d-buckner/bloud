import { test, expect } from '../lib/fixtures';

test.describe('App Uninstallation UI', () => {
  test('context menu has Remove App option', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(1000);

    // Right-click on app
    const appSlot = page.locator('.app-slot').first();
    await appSlot.click({ button: 'right' });

    // Should show uninstall option
    await expect(page.locator('.context-menu')).toBeVisible({ timeout: 3000 });
    await expect(page.getByText('Remove App')).toBeVisible();
  });

  test('clicking Remove App shows confirmation modal', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(1000);

    await page.locator('.app-slot').first().click({ button: 'right' });
    await page.getByText('Remove App').click();

    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 3000 });
  });

  test('can cancel uninstall', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(1000);

    await page.locator('.app-slot').first().click({ button: 'right' });
    await page.getByText('Remove App').click();

    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 3000 });
    await page.getByRole('button', { name: /cancel/i }).click();

    // Modal should close, app still present
    await page.waitForTimeout(500);
    await expect(page.locator('.app-slot').first()).toBeVisible();
  });
});

// Slow tests that actually uninstall/reinstall - skip by default
test.describe('App Uninstall Flow', () => {
  test.skip('can uninstall and reinstall app', async ({ trackedPage: page, api }) => {
    await api.ensureAppRunning('actual-budget');

    await page.goto('/');
    await page.waitForTimeout(1000);

    // Uninstall
    const appSlot = page.locator('.app-slot', { hasText: 'actual-budget' });
    await appSlot.click({ button: 'right' });
    await page.getByText('Remove App').click();
    await page.getByRole('button', { name: /uninstall|confirm|remove/i }).click();

    await api.waitForAppUninstalled('actual-budget', 60000);

    // Verify gone
    await page.goto('/');
    await expect(page.locator('.app-slot', { hasText: 'actual-budget' })).not.toBeVisible();

    // Reinstall from catalog
    await page.goto('/catalog');
    await page.getByText('Actual Budget').first().click();
    await page.getByRole('button', { name: /install/i }).click();

    await api.waitForAppStatus('actual-budget', 'running', 60000);
  });
});
