// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test';
import { HarnessSession } from '../src/index';

/**
 * The agentic recipes: a small set of observations an agent or a person runs
 * against a live instance without writing a spec.
 *
 * `bin/browser.mjs` is the entry. It sets the environment below and runs this
 * file through Playwright, so the recipe gets a real browser, the same
 * Playwright settings the gate suite uses, and the same harness code. Running
 * the file with no recipe is a deliberate no-op, so the config is safe to
 * invoke by hand.
 *
 *   BLOUD_HARNESS_RECIPE     shot | review | verify
 *   BLOUD_HARNESS_TARGET     bloud | <app id> | <url>
 *   BLOUD_HARNESS_SIGNED_IN  "false" to stay anonymous
 *   BLOUD_HARNESS_OUT        screenshot directory
 *   BLOUD_HARNESS_FULL       "true" for a full-page screenshot
 *   BLOUD_HARNESS_JSON       "true" for review output as JSON
 *   BLOUD_HARNESS_SETTLE_MS  settle time after navigation
 */
const recipe = process.env.BLOUD_HARNESS_RECIPE ?? '';
const target = process.env.BLOUD_HARNESS_TARGET ?? 'bloud';

test('browser harness recipe', async ({ page }) => {
  test.skip(!recipe, 'set BLOUD_HARNESS_RECIPE (use harness/bin/browser.mjs)');
  test.setTimeout(5 * 60_000);

  const session = new HarnessSession(page, {
    signedIn: process.env.BLOUD_HARNESS_SIGNED_IN !== 'false',
    settleMs: Number(process.env.BLOUD_HARNESS_SETTLE_MS ?? 1_500),
  });

  if (session.signedIn) {
    await session.signIn();
  }
  await session.open(target);

  if (recipe === 'shot') {
    const path = await session.screenshot(target, {
      fullPage: process.env.BLOUD_HARNESS_FULL === 'true',
    });
    const review = await session.review();
    console.log(`screenshot  ${path}`);
    console.log(`url         ${review.url}`);
    console.log(`title       ${review.title}`);
    return;
  }

  if (recipe === 'review') {
    const review = await session.review();
    if (process.env.BLOUD_HARNESS_JSON === 'true') {
      console.log(JSON.stringify(review));
      return;
    }
    console.log(`url         ${review.url}`);
    console.log(`title       ${review.title}`);
    console.log('--- text ---');
    console.log(review.text.slice(0, 4_000) || '(no visible text)');
    console.log('--- problems ---');
    console.log(session.diag.report());
    return;
  }

  if (recipe === 'verify') {
    const review = await session.review();
    const failures = session.diag.failures();
    console.log(`url         ${review.url}`);
    console.log(`title       ${review.title}`);
    console.log(session.diag.report());
    // The pending list is printed, not asserted: a healthy app can hold a
    // long-lived request (a WebSocket, a server-sent stream) open forever.
    expect(failures.map((p) => `${p.kind} ${p.detail}`), 'page problems').toEqual([]);
    return;
  }

  throw new Error(
    `unknown recipe "${recipe}": expected shot, review, or verify`,
  );
});
