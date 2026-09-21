// SPDX-License-Identifier: AGPL-3.0-only
//
// license-header: check or normalize SPDX license headers on tracked source files.
//
//   node scripts/license-header.mjs check   # exit 1 if any header is missing/stale
//   node scripts/license-header.mjs fix     # normalize headers in place
//
// The header is the single SPDX line only, in the language's comment style
// (see styleFor/headerLines). Per-file copyright lines were retired 2026-09:
// attribution lives in the LICENSE notice ("The Bloud Authors"), so a header
// never misattributes a contributor's file. fix mode strips any retired
// Copyright line found in the header area. Generated files (golden testdata,
// the runtime-managed Traefik routes file) and no-comment formats (JSON,
// go.mod/go.sum) are excluded.

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';

const SPDX = 'SPDX-License-Identifier: AGPL-3.0-only';

// Header-area shapes: the canonical SPDX line in any comment style, the
// retired per-file copyright line (old styles: `//`, `#`, `--`, ` * `, and
// the html variant that carried the closing `-->` on the same line), and the
// dangling closer left by the old three-line C-style header.
const SPDX_LINE = /^\s*(?:\/\/\s*|#\s*|--\s*|\/\*\s*|<!--\s*)SPDX-License-Identifier:/;
const COPYRIGHT_LINE = /^\s*(?:(?:\/\/|\*|#|--)\s+)?Copyright \(c\) \d{4}.*$/;
const CBLOCK_CLOSER = /^\s*\*\/\s*$/;

// Basenames that never carry a header (the two committed binaries should
// really be untracked; excluded here so this tool stays quiet).
const EXCLUDE_BASENAMES = new Set(['LICENSE', 'cli', 'host-agent']);

// Extensions that never carry a header (no-comment formats, binaries, docs).
const EXCLUDE_EXTS = new Set([
  '.md', '.json', '.sum', '.mod', '.lock', '.txt', '.bin',
  '.svg', '.png', '.jpg', '.gif', '.ico', '.woff', '.woff2',
  '.gitignore',
]);

// Generated / fixture files: their content is owned by the code that
// emits them, so a header would drift (or have to be mirrored in goldens).
const EXCLUDE_PATHS = new Set();

function isGenerated(file) {
  return file.endsWith('.golden.yml') || file.includes('/testdata/');
}

function headerLines(style) {
  switch (style) {
    case 'slash': return [`// ${SPDX}`];
    case 'hash': return [`# ${SPDX}`];
    case 'dash': return [`-- ${SPDX}`];
    case 'cblock': return [`/* ${SPDX} */`];
    case 'html': return [`<!-- ${SPDX} -->`];
    default: throw new Error(`unknown header style: ${style}`);
  }
}

function styleFor(file) {
  if (file.endsWith('.go') || file.endsWith('.ts') || file.endsWith('.js') ||
      file.endsWith('.mjs') || file.endsWith('.svelte')) return 'slash';
  if (file.endsWith('.py') || file.endsWith('.yaml') || file.endsWith('.yml') ||
      file.endsWith('.toml') || file.endsWith('.sh') || file.startsWith('.husky/')) return 'hash';
  if (file.endsWith('.sql')) return 'dash';
  if (file.endsWith('.css')) return 'cblock';
  if (file.endsWith('.html')) return 'html';
  return null;
}

function extOf(file) {
  const base = file.split('/').pop();
  const i = base.lastIndexOf('.');
  return i > 0 ? base.slice(i) : '';
}

function isHeaderLine(line) {
  return SPDX_LINE.test(line) || COPYRIGHT_LINE.test(line) || CBLOCK_CLOSER.test(line);
}

// The canonical form of `content`, or null when the file must be skipped.
// Strips any header already at the anchor (SPDX lines, retired per-file
// Copyright lines, the old cblock closer) and inserts the single-line SPDX
// header. Idempotent: canonicalizing a canonical file changes nothing.
function canonical(file, content) {
  const style = styleFor(file);
  if (!style) return null;
  const lines = content.split('\n');

  // Insertion anchor: after the line the header must trail.
  let at = 0;
  if (file.endsWith('.svelte')) {
    // Inside the <script> block so it is unambiguously a code comment and
    // never part of the component markup.
    const idx = lines.findIndex((l) => /^\s*<script[^>]*>/.test(l));
    if (idx === -1) return null;
    at = idx + 1;
  } else if (file.endsWith('.html')) {
    at = /^<!DOCTYPE/i.test(lines[0] ?? '') ? 1 : 0;
  } else if (lines[0]?.startsWith('#!')) {
    at = 1;
  }

  while (at < lines.length && isHeaderLine(lines[at])) lines.splice(at, 1);

  if (file.endsWith('.go')) {
    // Exactly one blank line after the header keeps it a separate comment
    // group from any //go:build constraint or doc comment that follows
    // (go/build requires build constraints to be separated by a blank line).
    while (lines[at] === '') lines.splice(at, 1);
    lines.splice(at, 0, '');
  }

  lines.splice(at, 0, ...headerLines(style));
  return lines.join('\n');
}

function main() {
  const mode = process.argv[2] === 'fix' ? 'fix' : 'check';
  const files = execFileSync('git', ['ls-files'], { encoding: 'utf8' })
    .split('\n').filter(Boolean);

  let bad = 0;
  let fixed = 0;
  for (const f of files) {
    const base = f.split('/').pop();
    const ext = extOf(f);
    if (EXCLUDE_BASENAMES.has(base) || EXCLUDE_EXTS.has(ext) ||
        EXCLUDE_PATHS.has(f) || isGenerated(f) || styleFor(f) === null) continue;

    let content;
    try {
      content = readFileSync(f, 'utf8');
    } catch {
      continue;
    }
    if (content.includes('\u0000')) continue; // binary

    const want = canonical(f, content);
    if (want === null) {
      if (mode === 'fix') console.log(`skipped (no insertion anchor): ${f}`);
      continue;
    }
    if (want === content) continue;

    if (mode === 'check') {
      const present = lines12(content).some((l) => SPDX_LINE.test(l));
      console.log(`${present ? 'stale license header' : 'missing license header'}: ${f}`);
      bad++;
    } else {
      writeFileSync(f, want);
      console.log(`fixed: ${f}`);
      fixed++;
    }
  }

  if (mode === 'check') {
    if (bad > 0) {
      console.error(`\n${bad} file(s) with a missing or stale license header (run: npm run license:fix)`);
      process.exit(1);
    }
    console.log('license headers: all tracked source files OK');
  } else {
    console.log(`\nfixed ${fixed} file(s)`);
  }
}

function lines12(content) {
  return content.split('\n').slice(0, 12);
}

main();
