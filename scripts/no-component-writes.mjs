// SPDX-License-Identifier: AGPL-3.0-only
//
// no-component-writes: fail if a component calls a mutating client function.
//
//   node scripts/no-component-writes.mjs [file ...]
//
// Every write in the dashboard goes through `src/lib/store/commands/`, which owns
// the dedupe key, the optimistic overlay, the settle rule and the toast. A
// component that imports `setPublicURL` from the client module and calls it
// directly skips all four: no claim, no wait, and no single place that says what
// a failed save looks like. That is the shape the refactor replaced, so the check
// is what keeps it from coming back.
//
// The table below is the registry. A new mutating client function must be added
// here before a component can use it, and an entry that no longer matches
// anything fails the run, so a rename cannot leave a stale hole. Reads stay
// allowed for now: moving every read into the store is plan phase 3 and 4.
//
// This check is deliberately narrower than "clients only under store/". The wider
// rule needs every read migrated first, and a check nobody can satisfy today is a
// check that gets an exception list instead of a fix.

import { readFileSync } from 'node:fs';
import { workingFiles } from './checkTargets.mjs';

const FRONTEND = 'services/host-agent/web/src';

/** Where a write is allowed to live. */
const COMMANDS = `${FRONTEND}/lib/store/commands/`;

/**
 * The mutating functions, by the module that exports them.
 *
 * `owner` is printed on every run so the table reads as a map of the write
 * surface rather than a list of words.
 */
const WRITES = [
  { name: 'installApp', owner: 'appClient' },
  { name: 'uninstallApp', owner: 'appClient' },
  { name: 'renameApp', owner: 'appClient' },
  { name: 'setPublicURL', owner: 'settingsClient' },
  { name: 'setTimezone', owner: 'settingsClient' },
  { name: 'setAISettings', owner: 'settingsClient' },
  { name: 'setEndpointSettings', owner: 'settingsClient' },
  { name: 'addExternalApp', owner: 'settingsClient' },
  { name: 'addExternalProvider', owner: 'settingsClient' },
  { name: 'updateExternalApp', owner: 'settingsClient' },
  { name: 'removeExternalApp', owner: 'settingsClient' },
  { name: 'createUser', owner: 'userClient' },
  { name: 'deleteUser', owner: 'userClient' },
  { name: 'setUserRole', owner: 'userClient' },
];

/**
 * The transport itself. These are not one call site, so their absence means
 * nothing yet, and they are exempt from the stale-entry rule. They stay on the
 * list because the first component that imports `put` directly is the hole this
 * check exists to close.
 */
const TRANSPORT = [
];

const WRITE_NAMES = new Set([...WRITES, ...TRANSPORT].map((entry) => entry.name));

/** `import { a, b as c } from '$lib/clients/x'`, including a multiline list. */
const CLIENT_IMPORT = /import\s*(?:type\s*)?\{([^}]*)\}\s*from\s*'\$lib\/clients\/[^']+'/g;

/** The imported names, without their local aliases. */
function importedNames(source) {
  const names = new Set();
  for (const match of source.matchAll(CLIENT_IMPORT)) {
    for (const part of match[1].split(',')) {
      const name = part.trim().split(/\s+as\s+/)[0].trim();
      if (name) names.add(name);
    }
  }
  return names;
}

const PATTERNS = [`${FRONTEND}/**/*.ts`, `${FRONTEND}/**/*.svelte`];
const explicit = (process.argv.slice(2) ?? []).filter((arg) => !arg.startsWith('-'));
// Whole-tree runs include files git does not know yet, so a new component cannot
// import a client write function and stay invisible until it is committed.
const files = explicit.length > 0 ? explicit : workingFiles(PATTERNS);

const seenWrites = new Set();
let problems = 0;

for (const file of files) {
  let source;
  try {
    source = readFileSync(file, 'utf8');
  } catch {
    continue;
  }

  const names = importedNames(source);
  for (const name of names) if (WRITE_NAMES.has(name)) seenWrites.add(name);

  if (file.startsWith(COMMANDS) || !file.startsWith(FRONTEND)) continue;

  for (const name of names) {
    if (!WRITE_NAMES.has(name)) continue;
    console.log(
      `${file}: imports the mutating client function "${name}". ` +
        'Writes belong in src/lib/store/commands/, behind a named operation.',
    );
    problems++;
  }
}

// An entry that matches nothing is a failure: it means the function was renamed
// or deleted and the table is now guarding a name nobody can call.
for (const entry of WRITES) {
  if (seenWrites.has(entry.name)) continue;
  console.log(`stale entry: "${entry.name}" (${entry.owner}) is never imported. Update the table.`);
  problems++;
}

// Print the map on every run, the same way the image-pin exceptions print: a
// guardrail that is invisible in the log is a guardrail nobody remembers.
console.log(
  `write surface: ${[...WRITES, ...TRANSPORT]
    .map((entry) => `${entry.name} (${entry.owner})`)
    .join(', ')}`,
);

if (problems > 0) {
  console.log(`\n${problems} problem(s). Every write goes through store/commands/.`);
  process.exit(1);
}
