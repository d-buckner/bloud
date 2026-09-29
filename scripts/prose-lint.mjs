// SPDX-License-Identifier: AGPL-3.0-only
//
// prose-lint: run Vale over the files that carry prose.
//
//   node scripts/prose-lint.mjs                 # every tracked prose file
//   node scripts/prose-lint.mjs docs/plans/x.md # just these
//
// Extracted from the `lint:prose` one-liner so the hook can pass staged files.
// The style package is provisioned on first use: `.vale/styles/Google` is not
// committed, so the first run pays a one-time `vale sync` and every later run
// reads it from disk. `.vale.ini` is the rule manifest; the pinned version here
// is the one that package was installed for.

import { execFileSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { targetsFromArgs } from './checkTargets.mjs';

const VALE = 'github.com/vale-cli/vale/v3/cmd/vale@v3.22.0';
const GOOGLE_STYLES = '.vale/styles/Google';
const PROSE_PATTERNS = [
  '*.md', '*.go', '*.ts', '*.js', '*.svelte',
  '*.yml', '*.yaml', '*.css', '*.html', '*.sql',
];

const files = targetsFromArgs(process.argv.slice(2), PROSE_PATTERNS);
if (files.length === 0) {
  console.log('prose-lint: no prose files to check');
  process.exit(0);
}

if (!existsSync(GOOGLE_STYLES)) {
  console.log(`prose-lint: ${GOOGLE_STYLES} missing, running 'vale sync' (one-time)`);
  execFileSync('go', ['run', VALE, 'sync', '--plain-progress'], { stdio: 'inherit' });
}

try {
  execFileSync('go', ['run', VALE, '--no-global', ...files], { stdio: 'inherit' });
} catch (err) {
  // Vale exits nonzero on error-severity alerts; its report already went to the
  // terminal via stdio: 'inherit', so just propagate the status.
  process.exit(err.status ?? 1);
}
