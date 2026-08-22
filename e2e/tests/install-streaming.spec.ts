// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { getAppStatus, uninstallApp, waitForApp } from '../lib/api';

// The user-visible live install state contract
// (plans/app-install-streaming.md):
//   - the catalog card shows the estimated download size
//   - install click -> the tile appears on the home grid within 2 s with a
//     live phase label (the install 202 carries the app record; no polling
//     round-trip)
//   - clicking the installing tile opens the live install modal with the
//     ordered step timeline
//   - the app converges to running and the tile reflects it
//
// The failure path (error display + Retry) is covered by the
// installTimeline/toasts unit tests: it requires fault injection, which the
// Go integration suite performs with `podman stop`
// (TestCrashRecoveryViaReconcile).
test.describe('live install state (install streaming)', () => {
  test.beforeEach(async ({ api }) => {
    if (await getAppStatus('jellyfin')) {
      await uninstallApp('jellyfin');
      const deadline = Date.now() + 5 * 60_000;
      while (Date.now() < deadline && (await getAppStatus('jellyfin'))) {
        await new Promise((r) => setTimeout(r, 3_000));
      }
    }
    expect(await getAppStatus('jellyfin')).toBeNull();
  });

  test('catalog card shows a size estimate', async ({ authenticatedPage }) => {
    const page = authenticatedPage;
    await page.goto('/catalog');
    const card = page.locator('.app-card', { hasText: 'Jellyfin' }).first();
    await expect(card).toBeVisible({ timeout: 15_000 });
    await expect(card.locator('.app-size')).toHaveText(/~1\.[0-9] GB/);
  });

  test('install click shows the tile immediately with live state', async ({
    authenticatedPage,
  }) => {
    const page = authenticatedPage;

    await page.goto('/catalog');
    await page.locator('.app-card', { hasText: 'Jellyfin' }).first().click();
    await page.getByRole('button', { name: 'Get' }).click();

    // The tile must appear on the home grid immediately — the install 202
    // carries the app record, so no polling round-trip is needed.
    await page.goto('/');
    const tile = page.locator('.app-slot', { hasText: 'Jellyfin' }).first();
    await expect(tile).toBeVisible({ timeout: 2_000 });

    // A phase label (or the brief pre-phase spinner) is visible while
    // installing.
    await expect(tile.locator('.phase-label, .install-spinner').first()).toBeVisible({
      timeout: 2_000,
    });

    // Clicking the installing tile opens the live install modal.
    await tile.click();
    const timeline = page.locator('.timeline');
    await expect(timeline).toBeVisible({ timeout: 10_000 });
    expect(await timeline.locator('.step-label').allTextContents()).toEqual([
      'Accepted',
      'Planned',
      'Pulling image',
      'Configuring',
      'Starting',
      'Finalizing',
      'Ready',
    ]);

    // The app converges (a fresh VM pulls the image, so this is the long
    // part). The budget stays under the 10 min per-test timeout.
    await waitForApp('jellyfin', 'running', 8 * 60_000);

    // The tile reflects running: no phase label, no spinner.
    await expect(tile.locator('.phase-label')).toHaveCount(0, { timeout: 30_000 });
    await expect(tile.locator('.install-spinner')).toHaveCount(0);
  });
});
