// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '@playwright/test';
import { BloudPage, HomeAssistantPage, probeWebSocketUpgrade } from '../lib/pom';

/**
 * A hand-run probe that uses the same page objects the suite uses.
 *
 *   BLOUD_DIAGNOSE=render    BLOUD_DIAGNOSE_URL=https://homeassistant.home.thebloud.org/ \
 *       npx playwright test diagnose
 *   BLOUD_DIAGNOSE=ws        BLOUD_URL=https://home.thebloud.org npx playwright test diagnose
 *   BLOUD_DIAGNOSE=ha-signin npx playwright test diagnose
 *   BLOUD_DIAGNOSE=bloud     npx playwright test diagnose
 *
 * It is a spec rather than a loose script on purpose: the runner already
 * compiles the TypeScript, applies the browser and proxy settings, keeps the
 * trace and the screenshot on failure, and, above all, resolves the same
 * selectors the gate tests resolve. A standalone script with its own browser
 * launch is how a manual look and CI disagreed about a working page.
 *
 * Skipped unless BLOUD_DIAGNOSE names a scenario, so it never runs in CI.
 */
const scenario = process.env.BLOUD_DIAGNOSE ?? '';
const target = process.env.BLOUD_DIAGNOSE_URL ?? '';

test.describe('diagnose (manual)', () => {
  test('probe', async ({ page }, testInfo) => {
    test.skip(!scenario, 'set BLOUD_DIAGNOSE to a scenario name to run a probe');
    test.setTimeout(10 * 60_000);

    if (scenario === 'render') {
      expect(target, 'set BLOUD_DIAGNOSE_URL').toBeTruthy();
      const probe = new HomeAssistantPage(page);
      await page.goto(target, { waitUntil: 'domcontentloaded', timeout: 60_000 });
      await page.waitForTimeout(8_000);
      console.log('final url :', probe.url);
      console.log('title     :', await probe.title());
      console.log('text      :', JSON.stringify((await probe.text()).slice(0, 600)));
      console.log('pending   :', probe.diag.pending.length ? probe.diag.pending.join('\n  ') : '(none)');
      console.log('--- problems ---\n' + probe.diag.report());
      console.log('screenshot:', await probe.shot('render'));
      return;
    }

    if (scenario === 'ws') {
      // Proxy-capability probe: does a WebSocket upgrade survive every hop
      // between here and the app? Needs no credentials, so it is the fastest
      // way to re-check a reverse-proxy change. Point BLOUD_URL at the origin
      // under test.
      const origin = new HomeAssistantPage(page).origin();
      const result = await probeWebSocketUpgrade(`${origin}/api/websocket`);
      console.log(`${result.ok ? 'UP  ' : 'DOWN'} ${origin}/api/websocket  ${result.detail}`);
      expect(result.ok, `WebSocket upgrade failed: ${result.detail}`).toBe(true);
      return;
    }

    if (scenario === 'ha-signin') {
      const ha = new HomeAssistantPage(page);
      await ha.open();
      console.log('start     :', await ha.describe());
      const stopped = await ha.signIn();
      console.log('stopped on:', stopped);
      console.log('final url :', ha.url);
      console.log('problems  :\n' + ha.diag.report());
      console.log('screenshot:', await ha.shot('signin'));
      expect(stopped, `sign-in stopped on "${stopped}"`).toBe('dashboard');
      await ha.expectDashboard();
      return;
    }

    if (scenario === 'bloud') {
      const bloud = new BloudPage(page);
      await bloud.signIn();
      console.log('tiles     :', (await bloud.tileLabels()).join(', '));
      console.log('screenshot:', await bloud.shot('home'));
      return;
    }

    expect(
      ['render', 'ws', 'ha-signin', 'bloud'].includes(scenario),
      `unknown BLOUD_DIAGNOSE "${scenario}"`,
    ).toBe(true);
    void testInfo;
  });
});
