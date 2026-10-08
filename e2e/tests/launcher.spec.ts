// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { addLauncher, listLaunchers, removeLauncher } from '../lib/api';

// An external app launcher is a tile that opens a URL and wires to nothing. The
// tile is added through the API (the settings form is a thin wrapper over the
// same endpoint), and it must show up on the home grid and open its URL.
test.describe('external app launcher', () => {
  test.beforeEach(async () => {
    for (const launcher of await listLaunchers()) {
      await removeLauncher(launcher.id);
    }
  });

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
