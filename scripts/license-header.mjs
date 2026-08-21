// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
//
// license-header: check or fix SPDX license headers on tracked source files.
//
//   node scripts/license-header.mjs check   # exit 1 if any file lacks the header
//   node scripts/license-header.mjs fix     # insert missing headers in place
//
// Header rules are per-language (see styleFor/fixFile). Generated files
// (golden testdata, the runtime-managed Traefik routes file) and
// no-comment formats (JSON, go.mod/go.sum) are excluded.

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';

const HOLDER = 'Daniel Buckner';
const YEAR = '2026';
const SPDX = 'SPDX-License-Identifier: AGPL-3.0-only';
const COPYRIGHT = `Copyright (c) ${YEAR} ${HOLDER}`;

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
const EXCLUDE_PATHS = new Set([
  'services/host-agent/internal/api/traefik/dynamic/apps-routes.yml',
]);

function isGenerated(file) {
  return file.endsWith('.golden.yml') || file.includes('/testdata/');
}

function headerLines(style) {
  switch (style) {
    case 'slash': return [`// ${SPDX}`, `// ${COPYRIGHT}`];
    case 'hash': return [`# ${SPDX}`, `# ${COPYRIGHT}`];
    case 'dash': return [`-- ${SPDX}`, `-- ${COPYRIGHT}`];
    case 'cblock': return [`/* ${SPDX}`, ` * ${COPYRIGHT}`, ` */`];
    case 'html': return [`<!-- ${SPDX}`, `     ${COPYRIGHT} -->`];
    default: throw new Error(`unknown header style: ${style}`);
  }
}

function styleFor(file) {
  if (file.endsWith('.go') || file.endsWith('.ts') || file.endsWith('.js') ||
      file.endsWith('.svelte')) return 'slash';
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

function isHeadered(lines) {
  const head = lines.slice(0, 12).join('\n');
  return head.includes(SPDX) && head.includes(COPYRIGHT);
}

// Returns the corrected content, or null when the file should be skipped.
function fixContent(file, content) {
  const style = styleFor(file);
  if (!style) return null;
  const lines = content.split('\n');
  const header = headerLines(style);

  if (file.endsWith('.svelte')) {
    // Inside the <script> block so it is unambiguously a code comment and
    // never part of the component markup.
    const idx = lines.findIndex((l) => /^\s*<script[^>]*>/.test(l));
    if (idx === -1) return null;
    lines.splice(idx + 1, 0, ...header);
  } else if (file.endsWith('.html')) {
    const at = /^<!DOCTYPE/i.test(lines[0] ?? '') ? 1 : 0;
    lines.splice(at, 0, ...header);
  } else if (file.endsWith('.go')) {
    // Blank line after the header keeps it a separate comment group from
    // any //go:build constraint or doc comment that follows (go/build
    // requires build constraints to be separated by a blank line).
    lines.splice(0, 0, ...header, '');
    while (lines[header.length + 1] === '') lines.splice(header.length + 1, 1);
  } else {
    const at = lines[0]?.startsWith('#!') ? 1 : 0;
    lines.splice(at, 0, ...header);
  }
  return lines.join('\n');
}

function main() {
  const mode = process.argv[2] === 'fix' ? 'fix' : 'check';
  const files = execFileSync('git', ['ls-files'], { encoding: 'utf8' })
    .split('\n').filter(Boolean);

  let missing = 0;
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

    if (mode === 'check') {
      if (!isHeadered(content.split('\n'))) {
        console.log(`missing license header: ${f}`);
        missing++;
      }
    } else {
      if (isHeadered(content.split('\n'))) continue;
      const out = fixContent(f, content);
      if (out === null) {
        console.log(`skipped (no insertion anchor): ${f}`);
        continue;
      }
      writeFileSync(f, out);
      console.log(`fixed: ${f}`);
      fixed++;
    }
  }

  if (mode === 'check') {
    if (missing > 0) {
      console.error(`\n${missing} file(s) missing the license header (run: npm run license:fix)`);
      process.exit(1);
    }
    console.log('license headers: all tracked source files OK');
  } else {
    console.log(`\nfixed ${fixed} file(s)`);
  }
}

main();
