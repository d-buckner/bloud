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

/** The searchable field. The listbox shares the label "Time zone", so this
 * targets the combobox role and not a bare label lookup. */
function zoneField(page: import('@playwright/test').Page) {
  return section(page).getByRole('combobox', { name: 'Time zone' });
}

/**
 * Choose a zone through the searchable field: type to filter, then click the row.
 *
 * The empty zone is the "No zone" row, which leads the list while the field is
 * empty and is hidden once a query is typed.
 */
async function chooseZone(page: import('@playwright/test').Page, zone: string) {
  const field = zoneField(page);
  await field.click();
  if (zone) {
    await field.fill(zone);
    await section(page).getByRole('option', { name: zone, exact: true }).click();
  } else {
    await field.fill('');
    await section(page).getByRole('option', { name: /No zone/ }).click();
  }
  await expect(field).toHaveValue(zone);
}

test.describe('time zone round trip (Settings -> Time zone)', () => {
  test('the field shows the live zone', async ({ authenticatedPage: page, api }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const field = zoneField(page);
    await expect(field).toBeVisible({ timeout: 15_000 });

    const live = await api.getTimezone();
    await expect(field).toHaveValue(live.zone);
  });

  // The search is the reason the control exists: a browser offers about 400
  // zones, and a city name is what an operator knows. Typing a city has to bring
  // the zone up and choosing it has to put that zone in the field.
  test('typing a city finds the zone and choosing it fills the field', async ({
    authenticatedPage: page,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const field = zoneField(page);
    await expect(field).toBeVisible({ timeout: 15_000 });

    await field.click();
    await field.fill('tokyo');
    await expect(section(page).getByRole('option', { name: 'Asia/Tokyo', exact: true })).toBeVisible();
    await section(page).getByRole('option', { name: 'Asia/Tokyo', exact: true }).click();
    await expect(field).toHaveValue('Asia/Tokyo');
  });

  test('the stored zone survives a save that changes nothing', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');
    await expect(zoneField(page)).toBeVisible({ timeout: 15_000 });

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

    const field = zoneField(page);
    await expect(field).toBeVisible({ timeout: 15_000 });
    const save = section(page).getByRole('button', { name: /^Save$/ });
    await expect(save).toBeDisabled();

    const before = await field.inputValue();
    // A zone every browser lists and the instance is certainly not on.
    const other = before === 'Asia/Tokyo' ? 'Europe/London' : 'Asia/Tokyo';
    await chooseZone(page, other);

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
    await chooseZone(page, before);
    await expect(save).toBeDisabled();
  });

  test('a form that matches the stored zone shows no restart warning', async ({
    authenticatedPage: page,
    api,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');
    await expect(zoneField(page)).toBeVisible({ timeout: 15_000 });

    // The warning is about a change that has not been made. On a clean form it
    // is noise, and noise here trains the operator to click through it.
    await expect(section(page).getByText(/recreates every container it manages/)).toHaveCount(0);

    // One Save for this section, and the field carries the stored value rather
    // than the browser's guess at it.
    await expect(section(page).getByRole('button', { name: /^Save$/ })).toHaveCount(1);
    const live = await api.getTimezone();
    await expect(zoneField(page)).toHaveValue(live.zone);
  });
});
