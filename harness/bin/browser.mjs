#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-only
//
// The agentic entry to the browser harness.
//
// An agent or a person runs one observation, or one small written check, against
// a live Bloud instance without writing a Playwright spec:
//
//   node harness/bin/browser.mjs review immich          what does this page show
//   node harness/bin/browser.mjs verify jellyfin        fail on a page problem
//   node harness/bin/browser.mjs status                 what is running, and why not
//   node harness/bin/browser.mjs run app-health jellyfin
//   node harness/bin/browser.mjs run ./my-check.mjs     run a check you wrote
//
// The launcher only turns arguments into the environment the recipe spec reads,
// then runs that spec through this package's own Playwright. It is plain
// JavaScript on purpose: the recipe needs the TypeScript transform, and the
// Playwright runner already provides it. Argument parsing lives in cli/args.mjs
// so it can be tested without a browser.
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, readdirSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgv, USAGE } from '../cli/args.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const pkgDir = resolve(here, '..');
const repoRoot = resolve(pkgDir, '..');
const checksDir = join(pkgDir, 'checks');

/** The built-in checks this package ships, as `name -> absolute path`. */
function builtinChecks() {
  if (!existsSync(checksDir)) return {};
  const found = {};
  for (const file of readdirSync(checksDir).sort()) {
    if (file.endsWith('.mjs')) found[file.replace(/\.mjs$/, '')] = join(checksDir, file);
  }
  return found;
}

/**
 * Turn the `run` argument into a file to load.
 *
 * A name with no path and no extension is a built-in, so the common case is
 * `run app-health`. A path is the caller's own file, resolved against the
 * directory they ran from. A miss lists what exists, because the alternative is
 * an agent guessing names.
 */
function resolveCheck(name) {
  const builtins = builtinChecks();
  const looksLikePath = name.endsWith('.mjs') || name.endsWith('.js') || name.endsWith('.ts') || name.includes('/');
  if (!looksLikePath) {
    const builtin = builtins[name];
    if (builtin) return builtin;
    const list = Object.keys(builtins).join(', ') || '(none)';
    return { error: `no built-in check "${name}". Available: ${list}. Or name a file: run ./my-check.mjs` };
  }
  const file = resolve(process.cwd(), name);
  if (!existsSync(file)) return { error: `no check file at ${file}` };
  return file;
}

/** Print the built-in checks with the line each one declares. */
async function listChecks() {
  const builtins = builtinChecks();
  const names = Object.keys(builtins);
  if (names.length === 0) {
    console.log(`no built-in checks in ${checksDir}`);
    return 0;
  }
  const rows = [];
  for (const name of names) {
    let description = '';
    let target = '';
    try {
      const mod = await import(pathToFileURL(builtins[name]).href);
      description = mod.description ?? '';
      const declared = mod.options?.target ?? mod.default?.options?.target;
      target = declared ? ` [${declared}]` : '';
    } catch (err) {
      description = `failed to load: ${err.message}`;
    }
    rows.push([name + target, description]);
  }
  const width = Math.max(...rows.map((r) => r[0].length));
  console.log(`built-in checks (${checksDir})\n`);
  for (const [name, description] of rows) {
    console.log(`  ${name.padEnd(width)}  ${description}`);
  }
  console.log('\nrun one: node harness/bin/browser.mjs run <name> [target]');
  return 0;
}

const parsed = parseArgv(process.argv.slice(2));

if (parsed.help) {
  console.log(USAGE);
  process.exit(parsed.error ? 1 : 0);
}
if (parsed.error) {
  console.error(`${parsed.error}\n\n${USAGE}`);
  process.exit(2);
}

if (parsed.recipe === 'checks') {
  process.exit(await listChecks());
}

const env = { ...process.env, BLOUD_HARNESS_RECIPE: parsed.recipe };
// Only a target the caller named is passed down. A check file can name its own,
// and it cannot do that if the launcher always fills in the default.
if (parsed.targetGiven) env.BLOUD_HARNESS_TARGET = parsed.target;
if (parsed.options.anon) env.BLOUD_HARNESS_SIGNED_IN = 'false';
if (parsed.options.full) env.BLOUD_HARNESS_FULL = 'true';
if (parsed.options.json) env.BLOUD_HARNESS_JSON = 'true';
if (parsed.options.settleMs !== undefined) env.BLOUD_HARNESS_SETTLE_MS = String(parsed.options.settleMs);
if (parsed.options.timeoutMs !== undefined) env.BLOUD_HARNESS_TIMEOUT_MS = String(parsed.options.timeoutMs);
if (parsed.expects.length) env.BLOUD_HARNESS_EXPECTS = JSON.stringify(parsed.expects);
if (parsed.api) env.BLOUD_HARNESS_API = JSON.stringify(parsed.api);

if (parsed.recipe === 'run') {
  const file = resolveCheck(parsed.script);
  if (file.error) {
    console.error(file.error);
    process.exit(2);
  }
  env.BLOUD_HARNESS_SCRIPT = file;
  env.BLOUD_HARNESS_ARGS = JSON.stringify(parsed.args);
}

env.BLOUD_HARNESS_OUT ??= join(repoRoot, '.bloud', 'screenshots');
if (parsed.options.out) env.BLOUD_HARNESS_OUT = resolve(process.cwd(), parsed.options.out);
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
