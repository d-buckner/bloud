// SPDX-License-Identifier: AGPL-3.0-only
import type { Locator, Page } from '@playwright/test';
import { escapeRegExp } from './origin';
import { waitFor } from './wait';

/**
 * Locators for the Bloud dashboard itself: the catalog grid and the home grid.
 *
 * A display name is not a unique key, so a label never goes into `hasText`
 * bare. A string `hasText` matches a substring, and "Home Assistant" is a
 * substring of "Home Assistant MCP": the catalog sorts by catalog ID, which
 * puts `ha-mcp` before `homeassistant`, so `.first()` asserted against the
 * wrapper's card and reported an installed app as not installed. Anchoring
 * the pattern makes the label name exactly one app.
 *
 * The match is scoped to the element that carries the display name rather
 * than the whole card or tile, for the same reason in the other direction: a
 * card's text includes its description, and a description may name another
 * app (Prowlarr's lists the PVRs it syncs to).
 */
function exactLabel(label: string): RegExp {
  // The `\s` padding is load-bearing. A RegExp in `hasText` is tested against
  // the element's raw textContent, which is only normalized for *string*
  // matching, and the markup indents the name inside its element: the h3 in
  // CatalogAppCard renders as "\n\t\t\t\tHome Assistant\n\t\t\t". Anchors
  // alone match nothing.
  return new RegExp(`^\\s*${escapeRegExp(label.trim())}\\s*$`);
}

/** The one element of `kind` whose display name is exactly `label`. */
function byLabel(
  container: Page,
  kind: string,
  titleSelector: string,
  label: string,
): Locator {
  return container
    .locator(`.${kind}`)
    .filter({ has: container.locator(titleSelector, { hasText: exactLabel(label) }) })
    .first();
}

/** The catalog card for the app whose display name is exactly `label`. */
export function catalogCard(page: Page, label: string): Locator {
  return byLabel(page, 'app-card', '.app-title', label);
}

/** The home-screen tile for the app whose display name is exactly `label`. */
export function homeTile(page: Page, label: string): Locator {
  return byLabel(page, 'app-slot', '.app-label', label);
}

/**
 * Click an installed app's tile on the Bloud home screen and wait for the
 * app tab it opens (apps launch in a popup). The caller asserts on the
 * returned page: login screen or dashboard, depending on the app's auth
 * strategy and this context's cookie state.
 */
export async function openAppFromHome(page: Page, label: string): Promise<Page> {
  await page.goto('/');
  const tile = homeTile(page, label);
  await tile.waitFor({ state: 'visible', timeout: 15_000 });

  // The tile opens the app through its own click handler: it is a div with
  // role="button" rather than a link (GridStack's drag handler ignores
  // buttons), so a click that lands before SvelteKit hydrates the handler, or
  // one GridStack takes for a drag, does nothing at all. Retry until a popup
  // actually appears instead of failing the rung on the first attempt.
  const deadline = Date.now() + 30_000;
  for (;;) {
    const popupPromise = page
      .waitForEvent('popup', { timeout: 5_000 })
      .catch(() => null);
    await tile.click();
    const popup = await popupPromise;
    if (popup) {
      await popup.waitForLoadState();
      return popup;
    }
    if (Date.now() > deadline) {
      throw new Error(
        `clicking the ${label} tile did not open the app popup`,
      );
    }
  }
}

/** Assert the home-screen tile for a converged app: visible, with no live
 * install phase label or spinner. */
export async function assertRunningTile(page: Page, label: string): Promise<void> {
  const tile = homeTile(page, label);
  await tile.waitFor({ state: 'visible', timeout: 15_000 });
  await waitFor(
    `the ${label} tile to finish installing (an install spinner or phase label is still visible)`,
    async () =>
      (await tile.locator('.install-spinner').count()) === 0 &&
      (await tile.locator('.phase-label').count()) === 0,
    15_000,
  );
}

/** Assert the catalog lists the app and marks it installed. */
export async function assertInstalledInCatalog(page: Page, label: string): Promise<void> {
  await page.goto('/catalog');
  const card = catalogCard(page, label);
  await card.waitFor({ state: 'visible', timeout: 15_000 });
  await waitFor(
    `the catalog card for ${label} to be marked installed`,
    async () => ((await card.getAttribute('class')) ?? '').includes('installed'),
    15_000,
  );
}
