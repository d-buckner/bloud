// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test';

/**
 * Click an installed app's tile on the Bloud home screen and wait for the
 * app tab it opens (apps launch in a popup). The caller asserts on the
 * returned page: login screen or dashboard, depending on the app's auth
 * strategy and this context's cookie state.
 */
export async function openAppFromHome(page: Page, label: string): Promise<Page> {
  await page.goto('/');
  const popupPromise = page.waitForEvent('popup');
  await page.locator('.app-slot', { hasText: label }).first().click();
  const appPage = await popupPromise;
  await appPage.waitForLoadState();
  return appPage;
}

/**
 * Assert the home-screen tile for a converged app: visible, with no live
 * install phase label or spinner (a converged tile carries neither).
 */
export async function expectRunningTile(page: Page, label: string): Promise<void> {
  const tile = page.locator('.app-slot', { hasText: label }).first();
  await expect(tile).toBeVisible({ timeout: 15_000 });
  await expect(tile.locator('.install-spinner')).toHaveCount(0);
  await expect(tile.locator('.phase-label')).toHaveCount(0);
}

/**
 * Assert the catalog lists the app and marks it installed.
 *
 * The match is scoped to the card's title, not to the card's whole text: a
 * string `hasText` match searches the description too, and a description may
 * name another app (Prowlarr's mentions the PVRs it syncs to), which would
 * resolve to that app's card instead.
 */
export async function expectInstalledInCatalog(page: Page, label: string): Promise<void> {
  await page.goto('/catalog');
  const card = page
    .locator('.app-card')
    .filter({ has: page.locator('.app-title', { hasText: label }) })
    .first();
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card).toHaveClass(/installed/);
}
