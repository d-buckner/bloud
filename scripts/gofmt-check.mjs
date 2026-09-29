// SPDX-License-Identifier: AGPL-3.0-only
//
// gofmt-check: fail if any Go file is not gofmt-clean.
//
//   node scripts/gofmt-check.mjs                 # every tracked *.go
//   node scripts/gofmt-check.mjs a.go b.go       # just these
//
// This is `npm run check:gofmt` moved out of a one-liner so it can take a file
// list. npm appends `-- a b` to the end of the command string rather than into
// `$@`, so a script whose command ends in a `$(git ls-files ...)` cannot be
// narrowed; ending it in a program can.

import { execFileSync } from 'node:child_process';
import { targetsFromArgs } from './checkTargets.mjs';

const files = targetsFromArgs(process.argv.slice(2), ['*.go']);
if (files.length === 0) {
  console.log('gofmt: no Go files to check');
  process.exit(0);
}

let out = '';
try {
  out = execFileSync('gofmt', ['-l', ...files], { encoding: 'utf8' });
} catch (err) {
  // gofmt exits nonzero when it simply has something to report, and its stdout
  // is the list. Anything else is a real failure.
  out = err.stdout ? String(err.stdout) : '';
  if (!out && err.status !== 1) {
    console.error(`gofmt failed to run: ${err.message}`);
    process.exit(1);
  }
}

const unformatted = out.split('\n').filter(Boolean);
if (unformatted.length === 0) {
  console.log(`gofmt: ${files.length} file(s) clean`);
  process.exit(0);
}

console.log(`gofmt needed on ${unformatted.length} file(s):`);
console.log(unformatted.join('\n'));
console.log('\nRun: gofmt -w ' + unformatted.join(' '));
process.exit(1);
