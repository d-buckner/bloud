// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';

/**
 * The Settings -> Address round trip.
 *
 * The address is one field: scheme, host, and the port the proxy is dialed on,
 * all in the string the operator types. The bug this guards is the one the old
 * multi-host UI shipped with: the save carried the hostname but not the scheme,
 * so saving an https host wrote it back as http. Every derived URL (issuer,
 * redirect URIs, launch URLs) followed it off a TLS terminator, and the UI
 * still looked correct afterwards, so the only symptom was a login that stopped
 * working.
 *
 * The assertion is therefore not "the control exists" but "the value survives a
 * save", read back from the API rather than from the widget.
 */

function addressInput(page: import('@playwright/test').Page) {
  return page.getByLabel('Public address');
}

test.describe('address round trip (Settings -> Address)', () => {
  test('the field is populated with the live address', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const input = addressInput(page);
    await expect(input).toBeVisible({ timeout: 15_000 });

    const live = await api.getPublicURL();
    await expect(input).toHaveValue(live.url);
  });

  test('saving keeps the scheme and port; they do not silently revert', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const input = addressInput(page);
    await expect(input).toBeVisible({ timeout: 15_000 });

    const before = await api.getPublicURL();
    expect(before.url).toMatch(/^https?:\/\/[^/]+$/);

    // Re-enter the current value and save through the UI. The save is a real
    // write: if the request dropped the scheme or the port, the value read back
    // disagrees with what was submitted.
    await input.fill(before.url);
    const saveBtn = page.getByRole('button', { name: /^Save$/ });
    await expect(saveBtn).toBeEnabled({ timeout: 5_000 });
    await saveBtn.click();

    const after = await api.savePublicURLAndWait(before.url);
    expect(after.url, 'saving changed the stored address').toBe(before.url);
  });

  test('the address field is the only address control on the page', async ({
    authenticatedPage: page,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const section = page.locator('.address-section');
    await expect(section).toBeVisible({ timeout: 15_000 });

    // One input and one save button: no host list, no primary radio, no scheme
    // select, and no read-only alias line. The setting is a single URL, and
    // the built-in local aliases are not surfaced as anything to look at or
    // touch. Their reachability is covered by the hostset unit tests, which
    // assert AllBaseURLs still registers localhost and bloud.local.
    await expect(section.locator('input')).toHaveCount(1);
    await expect(section.locator('select')).toHaveCount(0);
    await expect(section.locator('button')).toHaveCount(1);
  });

  test('the save button is disabled until the field actually changes', async ({
    authenticatedPage: page,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const input = addressInput(page);
    await expect(input).toBeVisible({ timeout: 15_000 });
    const original = await input.inputValue();

    const saveBtn = page.getByRole('button', { name: /^Save$/ });
    await expect(saveBtn).toBeDisabled();

    await input.fill('https://changed.example.test');
    await expect(saveBtn).toBeEnabled();

    // Back to the stored value and the change is gone: dirty tracking compares
    // against what is saved, not against whether the field was ever touched.
    await input.fill(original);
    await expect(saveBtn).toBeDisabled();
  });

  test('an empty field cannot be submitted', async ({ authenticatedPage: page }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const input = addressInput(page);
    await expect(input).toBeVisible({ timeout: 15_000 });

    await input.fill('   ');
    await expect(page.getByRole('button', { name: /^Save$/ })).toBeDisabled();
  });
});
