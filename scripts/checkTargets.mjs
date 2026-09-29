// SPDX-License-Identifier: AGPL-3.0-only
//
// checkTargets: the file set a repo-wide check should run over.
//
// Every cheap check defaults to all tracked files, so `npm run check:gofmt`
// keeps its old meaning and stays a valid standalone command. The hooks pass an
// explicit list instead -- the staged files, or the range being pushed -- so a
// commit that touches one markdown file does not re-lint 535 of them.
//
// The distinction that matters is per-file versus whole-tree. A missing license
// header or an em dash lives in the file you changed, so scoping is free. A
// relative link is broken by the file you *deleted*, so `check:docs-links` and
// `check:image-pins` deliberately ignore this and keep scanning everything;
// both run in 0.2s, so there is nothing to win by narrowing them.

import { execFileSync } from 'node:child_process';
import { statSync } from 'node:fs';

// trackedFiles(['*.go']) -> tracked paths matching the git pathspec.
export function trackedFiles(patterns) {
  const args = ['ls-files'];
  if (patterns && patterns.length > 0) args.push('--', ...patterns);
  return execFileSync('git', args, { encoding: 'utf8' }).split('\n').filter(Boolean);
}

// existingOnly drops paths that are not a readable file on disk.
//
// This is not defensive padding. Vale treats a path it cannot open as "no paths
// given" and silently lints stdin instead, so one stale path turns a scoped
// prose check into a check of nothing at all that still reports success. Every
// caller that hands paths to an external tool wants this filter.
function existingOnly(paths) {
  return paths.filter((p) => {
    try {
      return statSync(p).isFile();
    } catch {
      return false;
    }
  });
}

// targetsFromArgs(explicit, patterns) resolves the list a check should use:
// the caller's explicit paths when it passed any, else every tracked file
// matching `patterns` (or every tracked file when patterns is empty).
export function targetsFromArgs(explicit, patterns) {
  const given = (explicit ?? []).filter((a) => !a.startsWith('-'));
  if (given.length > 0) return existingOnly(given);
  return existingOnly(trackedFiles(patterns));
}
