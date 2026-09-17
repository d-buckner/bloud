#!/usr/bin/env node
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
//
// no-adhoc-http: keep app integration code off raw net/http.
//
//   node scripts/no-adhoc-http.mjs        # exit 1 on any adhoc HTTP in apps
//
// Apps declare integration behaviour through pkg/appclient (typed requests,
// retries, auth, readiness waits). Hand-rolling http.NewRequest /
// http.DefaultClient / a raw http.Client.Do(req) reintroduces the exact
// failure modes appclient was built to eliminate (no timeout, ignored
// cancellation, ad-hoc status handling). This guard keeps the tree honest:
// any such call in apps/**/*.go (excluding _test.go) fails pre-commit unless
// it is explicitly allowlisted with a documented reason.
//
// appclient's own Call.Do(ctx) is the sanctioned terminal — it takes a
// context, not a request — so `.Do(ctx)` is deliberately NOT flagged.

import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';

// Explicit exceptions: file -> required human justification. Empty by design;
// an entry here is a visible, reviewed escape hatch, never a silent bypass.
// Format: 'apps/<name>/<file>.go': 'reason the bypass is necessary'
const ALLOWLIST = new Map();

// Raw-HTTP markers. Each entry: [label, RegExp].
const PATTERNS = [
  ['http.NewRequest', /http\.NewRequest/],
  ['http.DefaultClient', /http\.DefaultClient/],
  ['raw http.Client literal', /http\.Client\s*\{/],
  // http.Client.Do(request): a .Do( whose argument is NOT the context.
  // appclient's Call.Do(ctx) is allowed; .Do(req) / .Do(request) / .Do(r) are not.
  ['http.Client.Do(request)', /\.Do\((?!ctx\b)/],
];

// Strip a trailing // line comment, ignoring // that sits inside a string
// literal, so URLs like "http://host" don't confuse the scan.
function stripLineComment(line) {
  let inString = false;
  let quote = '';
  for (let i = 0; i < line.length; i++) {
    const ch = line[i];
    if (inString) {
      if (ch === '\\') { i++; continue; } // skip escaped char
      if (ch === quote) inString = false;
      continue;
    }
    if (ch === '"' || ch === '`' || ch === "'") { inString = true; quote = ch; continue; }
    if (ch === '/' && line[i + 1] === '/') return line.slice(0, i);
  }
  return line;
}

function scanFile(file) {
  let content;
  try {
    content = readFileSync(file, 'utf8');
  } catch {
    return [];
  }
  if (content.includes('\u0000')) return []; // binary

  const hits = [];
  const lines = content.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const code = stripLineComment(lines[i]);
    for (const [label, re] of PATTERNS) {
      if (re.test(code)) {
        hits.push({ line: i + 1, label, text: code.trim() });
      }
    }
  }
  return hits;
}

function main() {
  const files = execFileSync('git', ['ls-files', 'apps'], { encoding: 'utf8' })
    .split('\n').filter(Boolean)
    .filter((f) => f.endsWith('.go'))
    .filter((f) => !f.endsWith('_test.go'));

  const violations = [];
  for (const f of files) {
    if (ALLOWLIST.has(f)) continue;
    for (const h of scanFile(f)) {
      violations.push({ file: f, ...h });
    }
  }

  if (violations.length > 0) {
    console.error('\nadhoc HTTP in app integration code (use pkg/appclient instead):\n');
    for (const v of violations) {
      console.error(`  ${v.file}:${v.line}  [${v.label}]  ${v.text}`);
    }
    console.error(
      `\n${violations.length} violation(s). Apps call the HTTP surface through ` +
      `pkg/appclient (client.GET/POST(...).Wait/Do/Ensure). If a bypass is truly ` +
      `required, add the file to ALLOWLIST in scripts/no-adhoc-http.mjs with a reason.`,
    );
    process.exit(1);
  }
  console.log('no-adhoc-http: apps clean (all HTTP via pkg/appclient)');
}

main();
