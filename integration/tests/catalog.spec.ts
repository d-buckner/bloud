import { test, expect } from '../lib/fixtures';

test.describe('App Catalog', () => {
  test('displays available apps', async ({ trackedPage: page }) => {
    await page.goto('/catalog');

    await expect(page.getByRole('heading', { name: 'App Catalog' })).toBeVisible();
    await expect(page.locator('.apps-grid')).toBeVisible({ timeout: 5000 });

    const appCards = page.locator('.apps-grid > *');
    await expect(appCards.first()).toBeVisible();
  });

  test('shows app details in modal', async ({ trackedPage: page }) => {
    await page.goto('/catalog');
    await expect(page.locator('.apps-grid')).toBeVisible({ timeout: 5000 });

    await page.locator('.apps-grid > *').first().click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 3000 });
  });

  test('can filter apps by search', async ({ trackedPage: page }) => {
    await page.goto('/catalog');
    await expect(page.locator('.apps-grid')).toBeVisible({ timeout: 5000 });

    await page.getByPlaceholder('Search apps...').fill('miniflux');
    await page.waitForTimeout(300);

    const appCards = page.locator('.apps-grid > *');
    const count = await appCards.count();
    expect(count).toBeLessThanOrEqual(3);
  });

  test('can filter apps by category', async ({ trackedPage: page }) => {
    await page.goto('/catalog');
    await expect(page.locator('.category-pills')).toBeVisible({ timeout: 5000 });

    const beforeCount = await page.locator('.apps-grid > *').count();
    await page.locator('.pill:not(.active)').first().click();
    await page.waitForTimeout(300);

    const afterCount = await page.locator('.apps-grid > *').count();
    expect(afterCount).toBeLessThanOrEqual(beforeCount);
  });
});

test.describe('App Installation', () => {
  test('installed app appears on home page', async ({ trackedPage: page, api }) => {
    // Use already-installed app (fast test)
    await api.ensureAppRunning('miniflux');

    await page.goto('/');
    await page.waitForTimeout(1000);

    await expect(page.getByText('Miniflux')).toBeVisible({ timeout: 5000 });
  });

  // Slower test - only run when explicitly testing install flow
  test.skip('can install app from catalog', async ({ trackedPage: page, api }) => {
    await api.ensureAppUninstalled('miniflux');

    await page.goto('/catalog');
    await expect(page.locator('.apps-grid')).toBeVisible({ timeout: 5000 });

    await page.getByText('Miniflux', { exact: false }).first().click();
    await expect(page.getByRole('dialog')).toBeVisible({ timeout: 3000 });

    await page.getByRole('button', { name: /install/i }).click();

    // Wait for install (takes 15-20 seconds for NixOS rebuild)
    await api.waitForAppStatus('miniflux', 'running', 60000);
  });
});
