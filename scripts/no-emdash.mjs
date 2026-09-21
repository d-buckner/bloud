// SPDX-License-Identifier: AGPL-3.0-only
//
// no-emdash: fail if any tracked file contains an em dash (U+2014).
//
//   node scripts/no-emdash.mjs
//
// Vale enforces the same rule for Markdown and for source comments, with an
// editor-friendly message (`.vale/styles/Bloud/EmDash.yml`), but it cannot read
// string literals or `.mjs`, so this is the repo-wide gate: `npm run lint:prose`
// covers what Vale can see, this covers the rest.
//
// The `&mdash;` entity is not checked here (Vale's rule covers it in prose, and
// the pattern would make this file fail its own check).

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

const EM_DASH = /\u2014/;

const files = execFileSync('git', ['ls-files'], { encoding: 'utf8' })
  .split('\n')
  .filter(Boolean);

let hits = 0;
for (const file of files) {
  let content;
  try {
    content = readFileSync(file, 'utf8');
  } catch {
    continue;
  }
  if (content.includes('\u0000')) continue; // binary

  content.split('\n').forEach((line, i) => {
    if (!EM_DASH.test(line)) return;
    console.log(`${file}:${i + 1}: em dash: ${line.trim().slice(0, 120)}`);
    hits++;
  });
}

if (hits > 0) {
  console.error(
    `\n${hits} line(s) contain an em dash. Use a colon, semicolon, comma, or parentheses.`,
  );
  process.exit(1);
}
console.log('no-emdash: no em dashes in tracked files');
