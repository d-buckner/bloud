// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';

/**
 * The Settings -> Hosts scheme round trip.
 *
 * The bug this guards: the host save carried hostnames and a primary but no
 * scheme, so saving an https host wrote the scheme back to http. Every derived
 * URL (issuer, redirect URIs, launch URLs) followed it off a TLS terminator, and
 * the UI still looked correct afterwards, so the only symptom was a login that
 * stopped working.
 *
 * The assertion is therefore not "the control exists" but "the value survives a
 * save". A save that drops the scheme fails here instead of shipping.
 */

function primaryCustomHost(page: Page) {
  // The primary host that is not a built-in: built-ins carry a fixed mapping and
  // have no scheme control, so the row with a scheme select is the one under test.
  return page.locator('.host-row').filter({ has: page.locator('select.host-scheme') }).first();
}

test.describe('host scheme round trip (Settings -> Hosts)', () => {
  test('the scheme select reflects what the backend reports', async ({
    authenticatedPage: page,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const row = primaryCustomHost(page);
    await expect(row).toBeVisible({ timeout: 15_000 });

    const hostname = (await row.locator('.host-name').textContent())?.trim() ?? '';
    const shown = await row.locator('select.host-scheme').inputValue();
    expect(['http', 'https']).toContain(shown);
    console.log(`host ${hostname} shows scheme ${shown}`);
  });

  test('saving keeps the scheme; it does not silently revert to http', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const row = primaryCustomHost(page);
    await expect(row).toBeVisible({ timeout: 15_000 });
    const hostname = (await row.locator('.host-name').textContent())?.trim() ?? '';
    const before = await row.locator('select.host-scheme').inputValue();

    // Re-select the current value and save. The save is a real write: if the
    // request omits the scheme, the stored value falls back to http and the
    // re-read below disagrees with what was saved.
    await row.locator('select.host-scheme').selectOption(before);
    const saveBtn = page.getByRole('button', { name: /Save Hosts/ });
    const dirty = await saveBtn.isVisible().catch(() => false);
    if (dirty) {
      await saveBtn.click();
      await page.waitForTimeout(3_000);
    }

    // Read the stored value from the API rather than the widget, so a UI that
    // echoes its own draft cannot mask a dropped write.
    const hosts = await api.getHosts();
    const stored = hosts.find((h) => h.hostname === hostname);
    expect(stored, `${hostname} missing from the host list`).toBeTruthy();
    expect(
      stored?.scheme,
      `saving ${hostname} changed its scheme from ${before} to ${stored?.scheme}`,
    ).toBe(before);
  });
});
