// SPDX-License-Identifier: AGPL-3.0-only
//
// Argument parsing for `bin/browser.mjs`, kept out of the launcher so it can be
// tested without starting a browser.
//
// The grammar is small on purpose: a recipe, then a target, then options. A
// person or an agent should get a first command right from the help text alone.

export const RECIPES = ['shot', 'review', 'verify', 'apps', 'status', 'api', 'run', 'checks'];

/** Options that take a value, and where the value is stored. */
const VALUE_OPTIONS = {
  '--settle': 'settleMs',
  '--timeout': 'timeoutMs',
  '--out': 'out',
  '--expect-text': 'expectText',
  '--expect-title': 'expectTitle',
  '--expect-url': 'expectUrl',
};

const FLAG_OPTIONS = { '--anon': 'anon', '--full': 'full', '--json': 'json' };

export const USAGE = `Bloud browser harness

  node harness/bin/browser.mjs <recipe> [target] [options]

Look at a page
  shot   [target]             capture a screenshot and print its path
  review [target]             print the url, title, visible text, and problems
  verify [target]             the same review, and exit nonzero on a problem

  A target is 'bloud' (the dashboard, the default), an app id such as jellyfin,
  or any http(s) url.

    --anon                    do not sign in first
    --full                    full-page screenshot (shot)
    --json                    print the review as JSON
    --settle <ms>             wait this long after navigation (default 1500)
    --timeout <ms>            stop the run after this long (default 5m)
    --out <dir>               screenshot directory (default .bloud/screenshots)
    --expect-text <pattern>   fail unless the page text matches (repeatable)
    --expect-title <pattern>  fail unless the document title matches
    --expect-url <pattern>    fail unless the page url matches

  A pattern is a plain substring, or a regular expression written /like this/i.

Read the instance
  apps                        installed apps, their state, and any failure cause
  status                      health, setup, settings, and the app table
  api <METHOD> <path> [json]  one authenticated host-agent API call

Write your own check
  run <name|path> [target] [args...]
  checks                      list the built-in checks

  A check file default-exports one function and imports nothing. The runner
  hands it the page, the API client, and the assertions:

    export default async function ({ page, api, check, open }) {
      await api.ensureInstalled('jellyfin');
      await open('jellyfin');
      check.textIncludes(/Jellyfin/, 'the Jellyfin page renders');
      check.noProblems();
    }

  Run a file:   node harness/bin/browser.mjs run ./my-check.mjs
  Run a built-in: node harness/bin/browser.mjs run app-health jellyfin

Environment
  BLOUD_URL, BLOUD_APP_BASE_URL, BLOUD_API_URL, BLOUD_API_TOKEN,
  BLOUD_E2E_USERNAME, BLOUD_E2E_PASSWORD
`;

/**
 * Decide whether a typed pattern is a regular expression or a literal.
 *
 * `/source/flags` is a regular expression, and the flags are only the ones
 * JavaScript knows. That check matters: `--expect-url /if/flow` is a path a
 * person typed, not the regex `if/flo` with the flag `w`, and reading it as a
 * regex is how a plain question gets an answer nobody asked for.
 */
function toRegExp(source) {
  if (!source.startsWith('/')) return { pattern: source };
  const last = source.lastIndexOf('/');
  if (last < 1) return { pattern: source };
  const flags = source.slice(last + 1);
  if (!/^[dgimsuvy]*$/.test(flags)) return { pattern: source };
  try {
    new RegExp(source.slice(1, last), flags);
  } catch {
    return { error: `"${source}" is not a valid regular expression` };
  }
  return { pattern: source };
}

function needsValue(option, value, name) {
  if (value === undefined) return { error: `${option} needs a ${name}` };
  if (Number.isNaN(Number(value))) return { error: `${option} needs a number of milliseconds` };
  return { value };
}

/**
 * The result of one parse: either `error`, or the recipe and what it needs.
 *
 * @typedef {object} ParsedCommand
 * @property {string} [recipe] One of RECIPES.
 * @property {string} [target] `bloud`, an app id, or a url.
 * @property {boolean} [targetGiven] False when the caller named no target, which
 *   is how a check file gets to name its own.
 * @property {string} [script] The check name or path, for `run`.
 * @property {{method: string, path: string, body?: string}} [api] The call, for `api`.
 * @property {string[]} [args] Positional arguments after the target.
 * @property {{anon: boolean, full: boolean, json: boolean, settleMs?: number, timeoutMs?: number, out?: string}} [options]
 * @property {{kind: string, pattern: string}[]} [expects] The `--expect-*` list.
 * @property {boolean} [help] Print the usage.
 * @property {string} [error] Why the command line was refused.
 */

/**
 * Parse one command line.
 *
 * Returns `{ recipe, target, script, api, args, options, expects }`, or
 * `{ error }` for a command line the launcher should refuse rather than run.
 * A bad value is reported with the option that caused it: an agent reads the
 * message once and fixes the command, so the message names the option.
 *
 * @param {string[]} argv
 * @returns {ParsedCommand}
 */
export function parseArgv(argv) {
  if (argv.length === 0) return { help: true, error: 'a recipe is required' };
  if (argv[0] === '-h' || argv[0] === '--help') return { help: true };

  const [recipe, ...rest] = argv;
  if (!RECIPES.includes(recipe)) {
    return { error: `unknown recipe "${recipe}"` };
  }

  const options = { anon: false, full: false, json: false };
  const expects = [];
  const positionals = [];
  const repeatable = { expectText: [], expectTitle: [], expectUrl: [] };

  for (let i = 0; i < rest.length; i++) {
    const arg = rest[i];
    if (Object.hasOwn(FLAG_OPTIONS, arg)) {
      options[FLAG_OPTIONS[arg]] = true;
      continue;
    }
    if (Object.hasOwn(VALUE_OPTIONS, arg)) {
      const key = VALUE_OPTIONS[arg];
      const raw = rest[++i];
      if (raw === undefined) return { error: `${arg} needs a value` };
      if (key === 'settleMs' || key === 'timeoutMs') {
        const checked = needsValue(arg, raw, 'number');
        if (checked.error) return checked;
        options[key] = Number(raw);
        continue;
      }
      if (key === 'out') {
        options.out = raw;
        continue;
      }
      const parsed = toRegExp(raw);
      if (parsed?.error) return { error: parsed.error };
      repeatable[key].push(raw);
      continue;
    }
    if (arg.startsWith('-')) return { error: `unknown option "${arg}"` };
    positionals.push(arg);
  }

  for (const [key, kind] of [
    ['expectText', 'text'],
    ['expectTitle', 'title'],
    ['expectUrl', 'url'],
  ]) {
    for (const pattern of repeatable[key]) expects.push({ kind, pattern });
  }

  const result = { recipe, options, expects, args: [], target: 'bloud' };

  if (recipe === 'api') {
    // `api /api/health` is the whole call: a first word that starts with a slash
    // is a path, not a method, and GET is what a person means by it.
    const words = [...positionals];
    const method = words.length > 0 && !words[0].startsWith('/') ? words.shift() : 'GET';
    const [path, body] = words;
    if (!path) return { error: 'the api recipe needs a path: api GET /api/health' };
    result.api = { method: method.toUpperCase(), path, body };
    return result;
  }

  if (recipe === 'run') {
    const [script, target, ...args] = positionals;
    if (!script) return { error: 'the run recipe needs a check name or path: run app-health jellyfin' };
    result.script = script;
    result.target = target ?? 'bloud';
    result.targetGiven = Boolean(target);
    result.args = args;
    return result;
  }

  if (recipe === 'apps' || recipe === 'status' || recipe === 'checks') return result;

  if (positionals.length > 1) return { error: `unexpected extra argument "${positionals[1]}"` };
  result.target = positionals[0] ?? 'bloud';
  result.targetGiven = positionals.length > 0;
  return result;
}
