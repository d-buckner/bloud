// SPDX-License-Identifier: AGPL-3.0-only
import { existsSync } from 'node:fs';
import { basename } from 'node:path';
import { pathToFileURL } from 'node:url';
import { expect, test } from '@playwright/test';
import * as harness from '../src/index';
import {
  Check,
  HarnessSession,
  collectStatus,
  formatAppTable,
  formatApiResponse,
  formatStatus,
  listInstalledApps,
  request,
  resolveCheck,
  runCheckFile,
  toPattern,
} from '../src/index';

/**
 * The agentic recipes: the observations and the check runner an agent or a
 * person runs against a live instance without writing a spec.
 *
 * `bin/browser.mjs` is the entry. It sets the environment below and runs this
 * file through Playwright, so a recipe gets a real browser, the settings the
 * gate suite uses, and the same harness code. Running the file with no recipe
 * is a deliberate no-op, so the config is safe to invoke by hand.
 *
 *   BLOUD_HARNESS_RECIPE      shot | review | verify | apps | status | api | run
 *   BLOUD_HARNESS_TARGET      bloud | <app id> | <url>
 *   BLOUD_HARNESS_SIGNED_IN   "false" to stay anonymous
 *   BLOUD_HARNESS_OUT         screenshot directory
 *   BLOUD_HARNESS_FULL        "true" for a full-page screenshot
 *   BLOUD_HARNESS_JSON        "true" for machine-readable output
 *   BLOUD_HARNESS_SETTLE_MS   settle time after navigation
 *   BLOUD_HARNESS_TIMEOUT_MS  wall-clock budget for the run
 *   BLOUD_HARNESS_EXPECTS     JSON [{ kind, pattern }] from --expect-*
 *   BLOUD_HARNESS_SCRIPT      the check file, for `run`
 *   BLOUD_HARNESS_ARGS        JSON [string], positional arguments after the target
 *   BLOUD_HARNESS_API         JSON { method, path, body }, for `api`
 */
const recipe = process.env.BLOUD_HARNESS_RECIPE ?? '';
const target = process.env.BLOUD_HARNESS_TARGET ?? 'bloud';
const settleMs = Number(process.env.BLOUD_HARNESS_SETTLE_MS ?? 1_500);
const signedIn = process.env.BLOUD_HARNESS_SIGNED_IN !== 'false';
const timeoutMs = Number(process.env.BLOUD_HARNESS_TIMEOUT_MS ?? 5 * 60_000);
const asJson = process.env.BLOUD_HARNESS_JSON === 'true';
const expects: { kind: string; pattern: string }[] = JSON.parse(process.env.BLOUD_HARNESS_EXPECTS ?? '[]');

const BROWSER_RECIPES = ['shot', 'review', 'verify', 'run'];
const API_RECIPES = ['apps', 'status', 'api'];

/** The three reads a `--expect-*` option can make, run through one collector. */
async function applyExpects(check: Check): Promise<void> {
  for (const expectation of expects) {
    const pattern = toPattern(expectation.pattern);
    const label = `--expect-${expectation.kind} ${expectation.pattern}`;
    if (expectation.kind === 'text') await check.textIncludes(pattern, label);
    else if (expectation.kind === 'title') await check.titleIs(pattern, label);
    else if (expectation.kind === 'url') await check.urlIs(pattern, label);
    else check.fail(label, `unknown expect kind "${expectation.kind}"`);
  }
}

test('api recipe', async () => {
  test.skip(!API_RECIPES.includes(recipe), 'set BLOUD_HARNESS_RECIPE (use harness/bin/browser.mjs)');
  test.setTimeout(2 * 60_000);

  if (recipe === 'apps') {
    const apps = await listInstalledApps();
    console.log(asJson ? JSON.stringify(apps) : formatAppTable(apps));
    return;
  }

  if (recipe === 'status') {
    const snapshot = await collectStatus();
    console.log(asJson ? JSON.stringify(snapshot) : formatStatus(snapshot));
    const unreadable = ['health', 'setup'].filter((key) => 'error' in (snapshot as never)[key]);
    expect(unreadable, 'the instance did not answer its own health reads').toEqual([]);
    return;
  }

  const call = JSON.parse(process.env.BLOUD_HARNESS_API ?? '{}');
  const response = await request(call.method, call.path, call.body);
  console.log(formatApiResponse(response));
  // A 4xx or 5xx is a real answer, and it is printed either way. It still exits
  // nonzero, so a scripted validation cannot read a refused call as a pass.
  expect(response.status, `${call.method} ${call.path} was refused`).toBeLessThan(400);
});

test('browser recipe', async ({ page }) => {
  test.skip(!BROWSER_RECIPES.includes(recipe), 'set BLOUD_HARNESS_RECIPE (use harness/bin/browser.mjs)');
  test.setTimeout(timeoutMs);

  const session = new HarnessSession(page, { signedIn, settleMs });

  if (recipe === 'run') {
    await runCheck(session);
    return;
  }

  if (session.signedIn) await session.signIn();
  await session.open(target);

  if (recipe === 'shot') {
    const path = await session.screenshot(target, { fullPage: process.env.BLOUD_HARNESS_FULL === 'true' });
    const review = await session.review();
    console.log(`screenshot  ${path}`);
    console.log(`url         ${review.url}`);
    console.log(`title       ${review.title}`);
    return;
  }

  const review = await session.review();
  const check = new Check({ name: `${recipe} ${target}`, page, diag: session.diag });
  if (recipe === 'verify') await check.noProblems('the page recorded no problems');
  await applyExpects(check);

  if (asJson) {
    console.log(JSON.stringify({ ...review, results: check.json().results }));
  } else {
    console.log(`url         ${review.url}`);
    console.log(`title       ${review.title}`);
    if (recipe === 'review') {
      console.log('--- text ---');
      console.log(review.text.slice(0, 4_000) || '(no visible text)');
      console.log('--- problems ---');
      console.log(session.diag.report());
    }
  }
  if (check.results.length > 0) console.log(check.format());
  // A review with no `--expect-*` is an observation, not a verdict: it prints and
  // exits 0. Only an assertion that ran can fail it.
  if (check.results.length > 0) expect(check.allPassed, check.summary()).toBe(true);
});

/**
 * Load one check file and run it.
 *
 * The module is imported here for its declared options (a check may name its own
 * target and budget), then handed to `runCheckFile`, which imports it again from
 * the module cache. A check that cannot be loaded is reported the same way a
 * failed assertion is: one table, one reason.
 */
async function runCheck(session: HarnessSession): Promise<void> {
  const file = process.env.BLOUD_HARNESS_SCRIPT ?? '';
  if (!existsSync(file)) throw new Error(`no check file at "${file}"`);

  const mod = (await import(pathToFileURL(file).href)) as Record<string, unknown>;
  const resolved = resolveCheck(mod, basename(file));
  test.setTimeout(resolved.options.timeoutMs ?? timeoutMs);

  const openTarget = process.env.BLOUD_HARNESS_TARGET ?? resolved.options.target ?? 'bloud';
  if (session.signedIn && resolved.options.signedIn !== false) await session.signIn();
  if (resolved.options.open !== false) await session.open(openTarget);

  const check = await runCheckFile(file, {
    session,
    target: openTarget,
    args: JSON.parse(process.env.BLOUD_HARNESS_ARGS ?? '[]'),
    harness,
    onEvent: (line) => console.log(`  note    ${line}`),
  });

  console.log(check.format());
  if (asJson) console.log(JSON.stringify(check.json()));
  expect(check.allPassed, check.summary()).toBe(true);
}
