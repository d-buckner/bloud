// SPDX-License-Identifier: AGPL-3.0-only
//
// no-component-commands: fail if a component reaches the command module.
//
//   node scripts/no-component-commands.mjs [file ...]
//
// The command module is the platform's write engine. A component that imports it
// can call any write in the dashboard, from any screen, with no rule about which
// screen owns which action. That is how a cascading write starts: one component
// decides to also install, also reload, also rename.
//
// So a component imports a hook instead. `useAppActions`, `useGridActions`,
// `useExternalAppActions` and `useDashboard` each expose the writes one kind of
// component owns, and `store/index.ts` no longer exports `commands` at all. This
// check closes the remaining door: the internal path.
//
// An exception would have to carry a reason and would print on every run. There
// are none. A write that a component needs belongs in a hook.

import { readFileSync } from 'node:fs';
import { workingFiles } from './checkTargets.mjs';

const FRONTEND = 'services/host-agent/web/src';
const PLATFORM = `${FRONTEND}/lib/store/`;

/** Sources that would expose the command module to a component. */
const COMMAND_SOURCES = new Set(['$lib/store', '$lib/store/commands', '$lib/store/commands/index']);

/** No exceptions. The table exists so a future one must be typed and reviewed. */
const EXCEPTIONS = [];

/** `import { a, b } from '$lib/store'`, including a multiline list. */
const IMPORT = /import\s*(?:type\s*)?\{([^}]*)\}\s*from\s*'([^']+)'/g;

function importedNames(list) {
  return list
    .split(',')
    .map((part) => part.trim().split(/\s+as\s+/)[0].trim())
    .filter(Boolean);
}

const PATTERNS = [`${FRONTEND}/**/*.svelte`, `${FRONTEND}/**/*.ts`];
const explicit = (process.argv.slice(2) ?? []).filter((arg) => !arg.startsWith('-'));
const files = (explicit.length > 0 ? explicit : workingFiles(PATTERNS)).filter(
  (file) => file.startsWith(FRONTEND) && !file.startsWith(PLATFORM),
);

const seen = new Set();
let problems = 0;

for (const file of files) {
  let source;
  try {
    source = readFileSync(file, 'utf8');
  } catch {
    continue;
  }

  for (const match of source.matchAll(IMPORT)) {
    const [, list, from] = match;
    if (!COMMAND_SOURCES.has(from)) continue;
    if (!importedNames(list).includes('commands')) continue;

    const excused = EXCEPTIONS.find((entry) => entry.file === file);
    if (excused) {
      seen.add(excused.file);
      continue;
    }

    console.log(
      `${file}: imports the command module. Use a hook: ` +
        'useAppActions, useGridActions, useExternalAppActions, or useDashboard.',
    );
    problems++;
  }
}

// An exception that matches nothing is a failure, so the table cannot rot.
for (const entry of EXCEPTIONS) {
  if (seen.has(entry.file)) continue;
  console.log(`unused exception: ${entry.file} (${entry.reason})`);
  problems++;
}

console.log(
  EXCEPTIONS.length === 0
    ? 'component commands: none. Every write goes through a hook.'
    : `component commands: ${EXCEPTIONS.map((entry) => entry.file).join(', ')}`,
);

if (problems > 0) process.exit(1);
