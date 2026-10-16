// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import * as api from '../lib/api';

/**
 * The transcription half of Settings -> AI, end to end.
 *
 * Two things here could break quietly, and both look like a working page.
 *
 * The first is the same failure the address setting used to have: a save that
 * rewrites what the operator typed instead of storing it. The endpoint's path is
 * load-bearing (`/v1` is part of the origin an SDK dials), so a save that
 * stripped it would leave every consumer dialing a 404 while the field still
 * showed what was typed. The assertion is therefore the value read back from the
 * API, not the value in the input.
 *
 * The second is the credential. The browser must never see the stored key, and
 * a blank key field means "keep what is stored" rather than "clear it". A UI
 * that echoed the key would put an operator's paid API key in page state, and a
 * UI that sent an empty string on every save would silently unconfigure a
 * working endpoint.
 *
 * The describe title carries the app name on purpose. CI sizes its browser runs
 * by grepping titles for the affected app, so a title naming only the settings
 * surface would never be selected by any matrix leg. Podly is the reason the
 * surface exists and the app whose leg should catch a regression here, so the
 * title says both.
 */

const ENDPOINT = 'https://transcribe.example.test/v1';
const MODEL = 'whisper-large-v3-turbo';

test.describe('podly transcription settings (Settings -> AI)', () => {
  test('the section loads and reports the stored endpoint', async ({
    authenticatedPage: page,
    api: apiLib,
  }) => {
    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');

    const section = page.locator('[data-contract="transcription"]');
    await expect(section).toBeVisible({ timeout: 15_000 });
    await expect(section.locator('#transcription-base-url')).toBeVisible();

    const live = await apiLib.getTranscriptionSettings();
    await expect(section.locator('#transcription-base-url')).toHaveValue(live.baseUrl);
  });

  test('the endpoint and model survive the save; the path does not get stripped', async ({
    authenticatedPage: page,
    api: apiLib,
  }) => {
    const before = await apiLib.getTranscriptionSettings();
    try {
      const saved = await apiLib.setTranscriptionSettings({
        baseUrl: ENDPOINT,
        name: 'E2E Transcription',
        model: MODEL,
        apiKey: 'sk-e2e-key-1',
      });

      expect(saved.baseUrl, 'the stored endpoint lost its path').toBe(ENDPOINT);
      expect(saved.model).toBe(MODEL);
      expect(saved.hasApiKey, 'the key was not stored').toBe(true);
      expect(saved.servedTo.length, 'no consumer is reported for the contract').toBeGreaterThan(0);
    } finally {
      await restore(apiLib, before);
    }
  });

  test('the browser is never handed the stored key', async ({
    authenticatedPage: page,
    api: apiLib,
  }) => {
    const before = await apiLib.getTranscriptionSettings();
    try {
      await apiLib.setTranscriptionSettings({ baseUrl: ENDPOINT, apiKey: 'sk-e2e-key-2' });

      await page.goto('/settings');
      await page.waitForLoadState('domcontentloaded');
      const section = page.locator('[data-contract="transcription"]');
      await expect(section).toBeVisible({ timeout: 15_000 });

      // The field is blank and marked "stored": the page knows a key exists
      // without knowing what it is.
      await expect(section.locator('#transcription-key')).toHaveValue('');
      // Exact, not substring: the hint under the field reads "Leave blank to
      // keep the stored key.", so a loose match resolves to two nodes and the
      // assertion becomes a strict-mode violation whenever a key is stored.
      await expect(section.getByText('stored', { exact: true })).toBeVisible();

      // And the API agrees: it reports presence, never the value.
      const live = await apiLib.getTranscriptionSettings();
      expect(live.hasApiKey).toBe(true);
      expect(JSON.stringify(live)).not.toContain('sk-e2e-key-2');
    } finally {
      await restore(apiLib, before);
    }
  });

  test('saving with a blank key keeps the stored one', async ({
    authenticatedPage: page,
    api: apiLib,
  }) => {
    const before = await apiLib.getTranscriptionSettings();
    try {
      await apiLib.setTranscriptionSettings({ baseUrl: ENDPOINT, apiKey: 'sk-e2e-key-3' });

      // The save the UI makes when the operator edits the model and leaves the
      // key field alone: no apiKey field at all in the payload.
      await page.goto('/settings');
      await page.waitForLoadState('domcontentloaded');
      const section = page.locator('[data-contract="transcription"]');
      await expect(section).toBeVisible({ timeout: 15_000 });

      await section.locator('#transcription-model').fill('whisper-1');
      await section.getByRole('button', { name: /^Save$/ }).click();
      await expect(page.getByText('Saved. Apps pick this up on their next pass.')).toBeVisible({
        timeout: 15_000,
      });

      const live = await apiLib.getTranscriptionSettings();
      expect(live.model).toBe('whisper-1');
      expect(live.hasApiKey, 'editing another field dropped the stored key').toBe(true);
    } finally {
      await restore(apiLib, before);
    }
  });

  test('removing clears the endpoint and the credential with it', async ({
    authenticatedPage: page,
    api: apiLib,
  }) => {
    await apiLib.setTranscriptionSettings({ baseUrl: ENDPOINT, apiKey: 'sk-e2e-key-4' });

    await page.goto('/settings');
    await page.waitForLoadState('domcontentloaded');
    const section = page.locator('[data-contract="transcription"]');
    await expect(section).toBeVisible({ timeout: 15_000 });

    await section.getByRole('button', { name: /^Remove$/ }).click();
    await expect(section.locator('#transcription-base-url')).toHaveValue('', { timeout: 15_000 });

    const live = await apiLib.getTranscriptionSettings();
    expect(live.configured).toBe(false);
    expect(
      live.hasApiKey,
      'a key left behind under a removed endpoint is a credential nothing owns',
    ).toBe(false);
  });
});

/**
 * Puts the setting back the way the suite found it.
 *
 * The e2e run is one worker over a shared instance, and the transcription
 * endpoint is instance-wide state: a spec that leaves a dead endpoint behind
 * hands the next run an app configured against a host that does not exist.
 */
async function restore(apiLib: typeof api, before: api.TranscriptionSettings): Promise<void> {
  if (!before.configured) {
    await apiLib.setTranscriptionSettings({ baseUrl: '' });
    return;
  }
  await apiLib.setTranscriptionSettings({
    baseUrl: before.baseUrl,
    name: before.name,
    model: before.model,
  });
}
