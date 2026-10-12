// SPDX-License-Identifier: AGPL-3.0-only
//
// no-legacy-store: fail if anything imports `svelte/store`.
//
//   node scripts/no-legacy-store.mjs [file ...]
//
// The frontend store binds to components through runes (`store/bindings.svelte.ts`
// exposes `select()`), not through the readable/writable contract Svelte 4
// components consumed with `$store`. That choice is the one that decides whether
// the store is the architecture or one more pattern a file may pick, and a single
// `import { readable } from 'svelte/store'` in a new module makes it the latter:
// the shim would work, the component would render, and nothing would complain.
//
// So the rule is absolute and the exception table is empty on purpose. Adding an
// entry means choosing the old binding back, in review, in writing.
//
// This is the guardrail `docs/plans/frontend-fsm.md` records as the one that
// keeps the runes binding from becoming optional.

import { readFileSync } from 'node:fs';
import { targetsFromArgs } from './checkTargets.mjs';

// Empty by design. An entry here is a decision to reintroduce the old binding,
// so it has to be typed by a human and defended in the diff.
const EXCEPTIONS = [];

const LEGACY = /from\s+['"]svelte\/store['"]/;

const files = targetsFromArgs(process.argv.slice(2), [
  'services/host-agent/web/src/**/*.ts',
  'services/host-agent/web/src/**/*.svelte',
  'services/host-agent/web/src/**/*.svelte.ts',
]);

const allowed = new Set(EXCEPTIONS.map((e) => e.file));
let hits = 0;

for (const file of files) {
  if (allowed.has(file)) continue;
  let content;
  try {
    content = readFileSync(file, 'utf8');
  } catch {
    continue;
  }

  content.split('\n').forEach((line, i) => {
    if (!LEGACY.test(line)) return;
    console.log(
      `${file}:${i + 1}: imports svelte/store. Bind to the store with select() from $lib/store.`,
    );
    hits++;
  });
}

// Same rule as every other exemption table here: one that guards nothing is a
// failure, so the list cannot outlive the file it was written for.
for (const e of EXCEPTIONS) {
  if (files.length === 0 || files.includes(e.file)) continue;
  console.log(`unused exception: ${e.file} (${e.reason})`);
  hits++;
}

if (hits > 0) {
  console.error(`\nno-legacy-store: ${hits} problem(s).`);
  process.exit(1);
}
console.log(`no-legacy-store: clean. ${EXCEPTIONS.length} exception(s).`);
