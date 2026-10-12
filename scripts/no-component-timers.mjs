// SPDX-License-Identifier: AGPL-3.0-only
//
// no-component-timers: fail if a Svelte component starts a timer.
//
//   node scripts/no-component-timers.mjs [file ...]
//
// A `setInterval` or `setTimeout` inside a component is a component deciding
// *when* the data changes, which is the same category error as a component
// deciding what a status means. It is also the part that cannot be tested,
// cannot be shared, and keeps firing after the thing it was watching is gone.
// Timers belong in `src/lib/store/effects/`, where the cadence is state the
// connection machine can see and a test can advance.
//
// This is the shape the plan records as its guardrail
// (`docs/plans/frontend-fsm.md`): the migration is only real if a component that
// re-learns a poll fails a check instead of quietly passing review.
//
// Timers that are genuinely local UI (a "copied" badge that clears itself, a
// focus call after a modal mounts) are not orchestration, but they are still
// declared below with a reason. The table is data, it prints on every run, and an
// entry that matches nothing is itself a failure, so the list cannot rot into a
// permanent excuse bin.

import { readFileSync } from 'node:fs';
import { targetsFromArgs } from './checkTargets.mjs';

const FRONTEND = 'services/host-agent/web/src';

// Every component that is allowed to hold a timer, and why. Delete an entry when
// the component stops needing one: the check fails a stale entry on purpose.
const EXCEPTIONS = [
  {
    file: `${FRONTEND}/lib/widgets/SystemStats.svelte`,
    reason:
      'widget refresh cadence. Plan phase 3 (widgets) moves it to store/effects/widgetPoll.ts, the same shape graphPoll.ts uses.',
  },
  {
    file: `${FRONTEND}/lib/widgets/Storage.svelte`,
    reason:
      'widget refresh cadence. Plan phase 3 (widgets) moves it to store/effects/widgetPoll.ts, the same shape graphPoll.ts uses.',
  },
  {
    file: `${FRONTEND}/lib/widgets/QuickNotes.svelte`,
    reason:
      'debounce for the widget autosave and the "saved" hold. Local to one textarea, not a read of shared state.',
  },
  {
    file: `${FRONTEND}/lib/components/GridStackGrid.svelte`,
    reason:
      'debounced reconcile against gridstack after a programmatic layout change; it is a third-party API timing, not a data poll.',
  },
  {
    file: `${FRONTEND}/lib/components/RenameModal.svelte`,
    reason: 'one-shot focus after the modal renders. DOM timing, no data.',
  },
  {
    file: `${FRONTEND}/lib/components/AddressSettingsSection.svelte`,
    reason:
      'hand-rolled convergence poll after saving the address. Plan phase 4 replaces it with the command layer settling on convergence.',
  },
  {
    file: `${FRONTEND}/lib/components/SetupWizard.svelte`,
    reason:
      'first-run wait for Authentik to come up, before a session exists to attach to. Plan phase 4 folds it into the session machine.',
  },
  {
    file: `${FRONTEND}/lib/components/ClientAccessPanel.svelte`,
    reason: 'clears the "copied" badge 2s after a clipboard write. Local UI, no data.',
  },
];

const TIMER = /(setInterval|setTimeout)\s*\(/g;

function hitsIn(content) {
  const found = new Map();
  for (const match of content.matchAll(TIMER)) {
    found.set(match[1], (found.get(match[1]) ?? 0) + 1);
  }
  return found;
}

const files = targetsFromArgs(process.argv.slice(2), [
  'services/host-agent/web/src/**/*.svelte',
]);

const seen = new Set();
let problems = 0;

for (const file of files) {
  let content;
  try {
    content = readFileSync(file, 'utf8');
  } catch {
    continue;
  }

  const hits = hitsIn(content);
  if (hits.size === 0) continue;

  for (const [kind, count] of hits) {
    const matched = EXCEPTIONS.filter(
      (e) => e.file === file && (e.pattern === undefined || e.pattern === kind),
    );
    if (matched.length > 0) {
      for (const e of matched) seen.add(`${e.file}#${e.pattern ?? 'any'}`);
      continue;
    }
    console.log(
      `${file}: ${count} ${kind} call(s). Timers belong in src/lib/store/effects/, not in a component.`,
    );
    problems++;
  }
}

// An exception that matches nothing is a failure: it means the component was
// cleaned up and the exemption outlived the thing it was excusing.
for (const e of EXCEPTIONS) {
  const key = `${e.file}#${e.pattern ?? 'any'}`;
  if (seen.has(key)) continue;
  console.log(`unused exception: ${key} (${e.reason})`);
  problems++;
}

if (problems > 0) {
  console.error(`\nno-component-timers: ${problems} problem(s).`);
  process.exit(1);
}

console.log(`no-component-timers: clean. ${EXCEPTIONS.length} declared exception(s):`);
for (const e of EXCEPTIONS) {
  console.log(`  ${e.file}${e.pattern ? ` (${e.pattern})` : ''}: ${e.reason}`);
}
