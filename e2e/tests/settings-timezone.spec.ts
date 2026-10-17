// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';

/**
 * The Settings -> Time zone round trip.
 *
 * The zone is the one setting that reaches every container, and it is only real
 * once the containers are recreated: a container's environment is fixed when the
 * runtime creates it. The failure this guards is the quiet one. The value gets
 * stored, the form agrees with itself, and every app keeps running on UTC.
 *
 * So the write test re-saves the zone that is already in force. The orchestrator
 * refuses a change that would change nothing, so this costs no restart, and it
 * still proves the request carries the zone the control shows. A save that
 * dropped the value on the way through reads back as a different zone and fails.
 *
 * The gating test never saves. A real zone change recreates every container on
 * the instance, which is not something a spec should do to a shared runtime to
 * check a checkbox.
 */

function section(page: import('@playwright/test').Page) {
  return page.locator('.timezone-section');
}

function zoneSelect(page: import('@playwright/test').Page) {
  return section(page).getByLabel('Time zone');
}

async function optionValues(page: import('@playwright/test').Page): Promise<string[]> {
  return zoneSelect(page)
    .locator('option')
    .evaluateAll((options) => options.map((o) => (o as HTMLOptionElement).value));
}

test.describe('time zone round trip (Settings -> Time zone)', () => {
  test('the select is populated with the live zone', async ({ authenticatedPage: page, api }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const select = zoneSelect(page);
    await expect(select).toBeVisible({ timeout: 15_000 });

    const live = await api.getTimezone();
    await expect(select).toHaveValue(live.zone);
  });

  test('the stored zone survives a save that changes nothing', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');
    await expect(zoneSelect(page)).toBeVisible({ timeout: 15_000 });

    // The form cannot submit an unchanged zone: Save stays disabled until the
    // selection differs. This is the API-level round trip, and it is what shows
    // the intent path applies a zone instead of dropping it, including the empty
    // zone a fresh instance carries.
    const before = await api.getTimezone();
    const after = await api.saveTimezoneAndWait(before.zone);
    expect(after.zone, 'saving the same zone changed it').toBe(before.zone);
  });

  test('a changed zone cannot be saved until the restart is acknowledged', async ({
    authenticatedPage: page,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const select = zoneSelect(page);
    await expect(select).toBeVisible({ timeout: 15_000 });
    const save = section(page).getByRole('button', { name: /^Save$/ });
    await expect(save).toBeDisabled();

    const before = await select.inputValue();
    const other = (await optionValues(page)).find((value) => value !== before);
    if (!other) {
      test.skip(true, 'the browser offers only one zone to choose from');
      return;
    }

    await select.selectOption(other);

    // The change is dirty, and dirty is not enough: the warning names what the
    // save does, and Save stays gated until the operator agrees to it.
    await expect(section(page).getByText(/recreates every container it manages/)).toBeVisible();
    await expect(save).toBeDisabled();

    await section(page).getByRole('checkbox').check();
    await expect(save).toBeEnabled();

    // Withdrawing the agreement gates Save again. An operator who agreed to
    // restart on one choice has not agreed to the next one.
    await section(page).getByRole('checkbox').uncheck();
    await expect(save).toBeDisabled();

    // Leave the page with the stored zone selected, so a later spec in the same
    // run reads the value it expects.
    await select.selectOption(before);
    await expect(save).toBeDisabled();
  });

  test('a form that matches the stored zone shows no restart warning', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');
    await expect(zoneSelect(page)).toBeVisible({ timeout: 15_000 });

    // The warning is about a change that has not been made. On a clean form it
    // is noise, and noise here trains the operator to click through it.
    await expect(section(page).getByText(/recreates every container it manages/)).toHaveCount(0);

    // One Save for this section, and the select carries the stored value rather
    // than the browser's guess at it.
    await expect(section(page).getByRole('button', { name: /^Save$/ })).toHaveCount(1);
    const live = await api.getTimezone();
    await expect(zoneSelect(page)).toHaveValue(live.zone);
  });
});
