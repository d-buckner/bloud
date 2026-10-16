#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-only
//
// The agentic entry to the browser harness.
//
// An agent or a person runs one observation against a live Bloud instance
// without writing a Playwright spec:
//
//   node harness/bin/browser.mjs shot jellyfin            screenshot an app
//   node harness/bin/browser.mjs shot bloud --full        full dashboard shot
//   node harness/bin/browser.mjs review immich            url, text, problems
//   node harness/bin/browser.mjs verify jellyfin          fail on page errors
//   node harness/bin/browser.mjs review https://x --anon  anonymous look
//
// The launcher only translates arguments into the environment the recipe spec
// reads, then runs that spec through this package's own Playwright. It is plain
// JavaScript on purpose: the recipe needs the TypeScript transform, and the
// Playwright runner already provides it.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(here, '..');
const repoRoot = resolve(pkgDir, '..');

const RECIPES = new Set(['shot', 'review', 'verify']);

const USAGE = `Bloud browser harness

  node harness/bin/browser.mjs <recipe> [target] [options]

Recipes
  shot      capture a screenshot and print its path
  review    print the page url, title, visible text, and problems
  verify    the same review, but exit nonzero when the page reports a problem

Target
  bloud            the dashboard (default)
  <app id>         an app's origin, e.g. jellyfin
  <url>            any http(s) url

Options
  --anon           do not sign in
  --full           full-page screenshot (shot only)
  --json           review output as JSON (review only)
  --settle <ms>    wait this long after navigation (default 1500)
  --out <dir>      screenshot directory (default .bloud/screenshots)
  -h, --help       show this help

Environment
  BLOUD_URL, BLOUD_API_URL, BLOUD_E2E_USERNAME, BLOUD_E2E_PASSWORD
`;

function fail(message) {
  console.error(message);
  process.exit(2);
}

const argv = process.argv.slice(2);
if (argv.length === 0 || argv[0] === '-h' || argv[0] === '--help') {
  console.log(USAGE);
  process.exit(argv.length === 0 ? 1 : 0);
}

const [recipe, ...rest] = argv;
if (!RECIPES.has(recipe)) {
  fail(`unknown recipe "${recipe}"\n\n${USAGE}`);
}

const env = { ...process.env, BLOUD_HARNESS_RECIPE: recipe };
let target = 'bloud';

for (let i = 0; i < rest.length; i++) {
  const arg = rest[i];
  const value = () => {
    const next = rest[++i];
    if (next === undefined) fail(`${arg} needs a value`);
    return next;
  };
  switch (arg) {
    case '--anon':
      env.BLOUD_HARNESS_SIGNED_IN = 'false';
      break;
    case '--full':
      env.BLOUD_HARNESS_FULL = 'true';
      break;
    case '--json':
      env.BLOUD_HARNESS_JSON = 'true';
      break;
    case '--settle':
      env.BLOUD_HARNESS_SETTLE_MS = value();
      break;
    case '--out':
      env.BLOUD_HARNESS_OUT = resolve(process.cwd(), value());
      break;
    default:
      if (arg.startsWith('-')) fail(`unknown option "${arg}"\n\n${USAGE}`);
      if (target !== 'bloud') fail(`unexpected extra argument "${arg}"`);
      target = arg;
  }
}
env.BLOUD_HARNESS_TARGET = target;

if (!env.BLOUD_HARNESS_OUT) {
  env.BLOUD_HARNESS_OUT = join(repoRoot, '.bloud', 'screenshots');
}
mkdirSync(env.BLOUD_HARNESS_OUT, { recursive: true });

const playwrightBin = join(pkgDir, 'node_modules', '.bin', 'playwright');
if (!existsSync(playwrightBin)) {
  console.error(
    'the browser harness is not installed: run `npm --prefix harness ci` ' +
      '(or `npm --prefix harness install`) and `npm --prefix harness run install-browsers`',
  );
  process.exit(1);
}

const result = spawnSync(playwrightBin, ['test', '--config', 'cli/playwright.config.ts'], {
  cwd: pkgDir,
  env,
  stdio: 'inherit',
});
process.exit(result.status ?? 1);
