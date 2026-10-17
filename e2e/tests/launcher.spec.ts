// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { addLauncher, listLaunchers, removeLauncher } from '@bloud/browser-harness';

// An external app launcher is a tile that opens a URL and wires to nothing. The
// tile is added through the API (the settings form is a thin wrapper over the
// same endpoint), and it must show up on the home grid and open its URL.
test.describe('external app launcher', () => {
  // A launcher is instance state, and the suite runs against a shared instance.
  // Clearing before each test alone left the tile the test added sitting on the
  // home grid after the run: that is how a "Bloud Catalog" tile ended up on a
  // developer's dashboard, looking like a product feature rather than a test
  // leftover. Clear after each test as well.
  async function clearLaunchers() {
    for (const launcher of await listLaunchers()) {
      await removeLauncher(launcher.id);
    }
  }

  test.beforeEach(clearLaunchers);
  test.afterEach(clearLaunchers);

  test('a launcher tile opens its URL', async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    // Bloud's own origin, so the popup assertion never depends on external
    // network reachability from the runner.
    const launcherURL = 'http://localhost:8080/catalog';
    await addLauncher({ name: 'Bloud Catalog', url: launcherURL });

    await page.goto('/');
    const tile = page.locator('.app-slot', { hasText: 'Bloud Catalog' }).first();
    await expect(tile).toBeVisible({ timeout: 15_000 });

    const popupPromise = page.waitForEvent('popup');
    await tile.click();
    const popup = await popupPromise;
    await popup.waitForURL(/localhost:8080\/catalog/);
  });
});
