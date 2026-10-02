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
import { existsSync, readdirSync, rmSync } from 'node:fs';
import { targetsFromArgs } from './checkTargets.mjs';

const VALE = 'github.com/vale-cli/vale/v3/cmd/vale@v3.22.0';
const GOOGLE_STYLES = '.vale/styles/Google';
const PROSE_PATTERNS = [
  '*.md', '*.go', '*.ts', '*.js', '*.svelte',
  '*.yml', '*.yaml', '*.css', '*.html', '*.sql',
];

// The sync is the only step in this check whose failure has nothing to do with
// the repo: it pulls Vale and its dependencies across the network, and a
// truncated download from the module proxy takes the whole check down. It took
// a CI job down exactly that way. Retry it, because the failure is transient
// and the second attempt almost always lands.
const SYNC_ATTEMPTS = 3;

// stylesInstalled is what a completed sync looks like: the directory exists
// and actually holds the style files.
function stylesInstalled() {
  try {
    return readdirSync(GOOGLE_STYLES).length > 0;
  } catch {
    return false;
  }
}

async function syncStyles() {
  console.log(`prose-lint: ${GOOGLE_STYLES} missing, running 'vale sync' (one-time)`);
  for (let attempt = 1; attempt <= SYNC_ATTEMPTS; attempt++) {
    try {
      execFileSync('go', ['run', VALE, 'sync', '--plain-progress'], { stdio: 'inherit' });
      if (stylesInstalled()) return;
      throw new Error(`'vale sync' exited 0 but ${GOOGLE_STYLES} is still empty`);
    } catch (err) {
      // Delete whatever the interrupted sync left. `existsSync` is what
      // decides whether to sync at all, so a half-populated styles tree
      // would read as installed on every later run and silently lint
      // against rules that never arrived.
      rmSync(GOOGLE_STYLES, { recursive: true, force: true });
      if (attempt === SYNC_ATTEMPTS) {
        console.error(`prose-lint: 'vale sync' failed ${SYNC_ATTEMPTS}x: ${err.message}`);
        console.error('prose-lint: nothing was installed; fix the network and re-run, or npm run lint:prose:sync');
        process.exit(1);
      }
      const backoffMs = attempt * 2000;
      console.warn(`prose-lint: 'vale sync' attempt ${attempt} failed (${err.message}), retrying in ${backoffMs / 1000}s`);
      await new Promise((resolve) => setTimeout(resolve, backoffMs));
    }
  }
}

const files = targetsFromArgs(process.argv.slice(2), PROSE_PATTERNS);
if (files.length === 0) {
  console.log('prose-lint: no prose files to check');
  process.exit(0);
}

if (!stylesInstalled()) {
  await syncStyles();
}

try {
  execFileSync('go', ['run', VALE, '--no-global', ...files], { stdio: 'inherit' });
} catch (err) {
  // Vale exits nonzero on error-severity alerts; its report already went to the
  // terminal via stdio: 'inherit', so just propagate the status.
  process.exit(err.status ?? 1);
}
