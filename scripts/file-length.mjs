// SPDX-License-Identifier: AGPL-3.0-only
//
// file-length: a ratcheting cap on how long one source file may be.
//
//   node scripts/file-length.mjs            # check; exit 1 on a violation
//   node scripts/file-length.mjs --update   # lower the baseline (never raises)
//   node scripts/file-length.mjs --list     # also print every exempt file
//
// Why a ratchet and not a plain cap. A hard cap of 500 fails on 9 files today,
// which makes the check red for reasons nobody introduced and therefore safe to
// ignore. A ratchet is red only for the change you just made: the files that are
// already too long are recorded with their current size, and the recorded number
// is the ceiling that file may not exceed. The list can only shrink, so the check
// is green now and gets stricter on its own every time someone splits a file.
//
// This is the same shape as the exception table in `pinned-images.mjs` and the
// completeness test in `internal/wire`: the exemption is data, it is reviewed in
// the diff, and an exemption that guards nothing is itself a failure. A stale
// entry is worse than no entry, because it is a written claim about the code that
// stopped being true.
//
// What counts as a line: a non-blank line that is not entirely a comment. A line
// of code with a trailing comment is a code line; a `//` line and a `/* */` body
// are not. That is the same accounting `funlen` uses with `ignore-comments`, so
// a documented file is not penalised for its documentation.
//
// Scope: Go, TypeScript, and Svelte. Go tests are excluded, for the reason
// already recorded in `.golangci.yml` for `funlen`: a table-driven test
// legitimately runs long, and the cap is about production code. Markdown is not
// covered because a long spec is not the same problem as a long module.
//
// `--update` deliberately cannot add an entry and cannot raise a number. A new
// file over the limit has to be split, or exempted by editing the block by hand,
// which puts the decision in the diff where a reviewer sees it. A ratchet that
// can be re-widened with one command is not a ratchet.

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const LIMIT = 500;

// Built from fragments so the literal marker text never appears in this file
// outside the generated block itself; otherwise the search would land on the
// constant definition instead of the baseline.
const MARK_START = ['//', 'begin', 'ratchet', 'baseline'].join(' ');
const MARK_END = ['//', 'end', 'ratchet', 'baseline'].join(' ');

const SELF = fileURLToPath(import.meta.url);

// begin ratchet baseline
// Each number is the ceiling for that file: it may not exceed it. Lower only.
// Regenerate with `npm run check:file-length:update`, which never raises a
// number and never adds a file. An entry for a file that is compliant or
// gone is a failure, so this list cannot keep a claim that stopped being true.
const BASELINE = {
  'services/host-agent/pkg/authentik/client.go': 1454,
  'services/host-agent/internal/engine/orchestrator/orchestrator.go': 1291,
  'cli/dev.go': 828,
  'services/host-agent/internal/api/settings_module.go': 751,
  'services/host-agent/web/src/routes/settings/+page.svelte': 684,
  'services/host-agent/internal/engine/orchestrator/pipeline.go': 624,
  'services/host-agent/internal/sso/blueprint.go': 561,
  'cli/depgraph.go': 520,
  'services/host-agent/internal/api/auth_module.go': 507,
};
// end ratchet baseline

// The files the cap governs. `html` adds `<!-- -->` to the comment syntax, which
// is what a Svelte template is made of.
const LANGS = [
  { name: 'go', match: (f) => f.endsWith('.go') && !f.endsWith('_test.go') },
  { name: 'ts', match: (f) => f.endsWith('.ts') },
  { name: 'svelte', match: (f) => f.endsWith('.svelte'), html: true },
];

// codeLines counts non-blank, non-comment lines.
//
// The scan is line-anchored: a line whose first non-space characters open a
// comment is a comment line. A `//` that appears mid-line inside a string is
// therefore still code, which is the correct reading of "excluding comments".
function codeLines(src, { html = false } = {}) {
  let count = 0;
  let inBlock = false;
  let inHtml = false;
  for (const raw of src.split('\n')) {
    const line = raw.trim();
    if (line === '') continue;
    if (inHtml) {
      if (line.includes('-->')) inHtml = false;
      continue;
    }
    if (html && line.startsWith('<!--')) {
      if (!line.includes('-->')) inHtml = true;
      continue;
    }
    if (inBlock) {
      if (line.includes('*/')) inBlock = false;
      continue;
    }
    if (line.startsWith('/*')) {
      if (!line.includes('*/')) inBlock = true;
      continue;
    }
    if (line.startsWith('//')) continue;
    count++;
  }
  return count;
}

function langOf(file) {
  return LANGS.find((l) => l.match(file)) ?? null;
}

function trackedSources() {
  const all = execFileSync('git', ['ls-files'], { encoding: 'utf8' })
    .split('\n')
    .filter(Boolean);
  const out = [];
  for (const f of all) {
    const lang = langOf(f);
    if (!lang) continue;
    let src;
    try {
      src = readFileSync(f, 'utf8');
    } catch {
      continue;
    }
    if (src.includes('\u0000')) continue; // binary
    out.push({ path: f, lang: lang.name, lines: codeLines(src, { html: !!lang.html }) });
  }
  return out;
}

// ------------------------------------------------------------- baseline I/O

const ENTRY_RE = /^\s*'((?:[^'\\]|\\.)+)':\s*(\d+),?\s*$/;

function parseBaseline(text) {
  const start = text.indexOf(MARK_START);
  const end = text.indexOf(MARK_END);
  if (start === -1 || end === -1 || end < start) {
    throw new Error('ratchet baseline block not found in this script');
  }
  const body = text
    .slice(start + MARK_START.length, end)
    .split('\n')
    .map((l) => l.trim())
    .filter((l) => l !== '' && !l.startsWith('//'));

  const map = new Map();
  for (const line of body) {
    if (line === 'const BASELINE = {' || line === 'const BASELINE = {};' || line === '};') continue;
    const m = line.match(ENTRY_RE);
    if (!m) {
      throw new Error(`unparsable baseline entry: ${line}`);
    }
    map.set(m[1], Number(m[2]));
  }
  return map;
}

function renderBaseline(map) {
  const entries = [...map.entries()].sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1));
  const rows = entries.map(([p, n]) => `  '${p}': ${n},`);
  const header = [
    MARK_START,
    '// Each number is the ceiling for that file: it may not exceed it. Lower only.',
    '// Regenerate with `npm run check:file-length:update`, which never raises a',
    '// number and never adds a file. An entry for a file that is compliant or',
    '// gone is a failure, so this list cannot keep a claim that stopped being true.',
    'const BASELINE = {',
  ];
  const footer = ['};', MARK_END];
  if (entries.length === 0) {
    return `${MARK_START}\n// Nothing is exempt: every governed file is at or under ${LIMIT} code lines.\nconst BASELINE = {};\n${MARK_END}`;
  }
  return [...header, ...rows, ...footer].join('\n');
}

function writeBaseline(map) {
  const src = readFileSync(SELF, 'utf8');
  const start = src.indexOf(MARK_START);
  const end = src.indexOf(MARK_END) + MARK_END.length;
  writeFileSync(SELF, src.slice(0, start) + renderBaseline(map) + src.slice(end), 'utf8');
}

// ------------------------------------------------------------------- update

function update() {
  const baseline = parseBaseline(readFileSync(SELF, 'utf8'));
  const actual = new Map(trackedSources().map((f) => [f.path, f.lines]));

  const next = new Map();
  const dropped = [];
  const lowered = [];
  for (const [path, recorded] of baseline) {
    const now = actual.get(path);
    if (now === undefined) {
      dropped.push([path, recorded, 'file is gone']);
      continue;
    }
    if (now <= LIMIT) {
      dropped.push([path, recorded, `now ${now}, under the limit`]);
      continue;
    }
    const capped = Math.min(recorded, now);
    if (capped < recorded) lowered.push([path, recorded, now]);
    next.set(path, capped);
  }

  writeBaseline(next);
  const added = [...actual].filter(
    ([p, n]) => n > LIMIT && !baseline.has(p),
  );

  console.log(`file-length: baseline rewritten for ${next.size} exempt file(s)`);
  for (const [p, from, to] of lowered) {
    console.log(`  lowered  ${p}: ${from} -> ${to}`);
  }
  for (const [p, from, why] of dropped) {
    console.log(`  dropped  ${p}: ${from} (${why})`);
  }
  if (added.length > 0) {
    console.log('  not added (a new over-limit file is a violation, not a baseline entry):');
    for (const [p, n] of added) console.log(`    ${p}: ${n}`);
  }
}

// ------------------------------------------------------------------- check

function check(listFlag) {
  const baseline = parseBaseline(readFileSync(SELF, 'utf8'));
  const files = trackedSources();
  const actual = new Map(files.map((f) => [f.path, f.lines]));

  const grown = [];
  const fresh = [];
  for (const f of files) {
    const recorded = baseline.get(f.path);
    // An exemption raises the ceiling to the recorded number; no exemption
    // means the plain limit applies.
    const cap = recorded === undefined ? LIMIT : recorded;
    if (f.lines <= cap) continue;
    if (recorded === undefined) fresh.push({ ...f, cap });
    else grown.push({ ...f, cap, recorded });
  }

  const stale = [];
  for (const [path, recorded] of baseline) {
    const now = actual.get(path);
    if (now === undefined) {
      stale.push({ kind: 'missing', path, recorded, now: null });
    } else if (now <= LIMIT) {
      stale.push({ kind: 'retired', path, recorded, now });
    } else if (now < recorded) {
      stale.push({ kind: 'shrunk', path, recorded, now });
    }
  }

  if (listFlag) {
    const exempt = files
      .filter((f) => baseline.has(f.path))
      .sort((a, b) => b.lines - a.lines);
    console.log(`file-length: ${exempt.length} file(s) exempt above ${LIMIT} code lines`);
    for (const f of exempt) {
      console.log(`  ${String(f.lines).padStart(5)} / ${baseline.get(f.path)}  ${f.lang.padEnd(6)}  ${f.path}`);
    }
    console.log('');
  }

  const failed = grown.length + fresh.length + stale.length > 0;

  if (grown.length > 0) {
    console.error(`\nfile-length: ${grown.length} file(s) grew past their recorded ceiling`);
    for (const f of grown) {
      console.error(
        `  ${f.path}: ${f.lines} code lines, ceiling ${f.recorded} (limit ${LIMIT})`,
      );
    }
    console.error(
      '\n  The new code does not have to live in the file that is already too\n' +
        '  long. Move it to a file that has room. If the growth is genuinely\n' +
        '  unavoidable, raise the number in the baseline block by hand so the\n' +
        '  decision shows up in the diff.',
    );
  }

  if (fresh.length > 0) {
    console.error(`\nfile-length: ${fresh.length} file(s) over ${LIMIT} code lines with no exemption`);
    for (const f of fresh) {
      console.error(`  ${f.path}: ${f.lines} code lines (${f.lang})`);
    }
    console.error(
      '\n  Split it. If this file legitimately cannot be smaller, add it to\n' +
        '  the baseline block in scripts/file-length.mjs with a reason in the\n' +
        '  commit message, so the exemption is reviewed rather than absorbed.',
    );
  }

  if (stale.length > 0) {
    console.error(`\nfile-length: ${stale.length} baseline entr(ies) no longer describe the tree`);
    for (const s of stale) {
      if (s.kind === 'missing') {
        console.error(`  ${s.path}: exempt at ${s.recorded} lines, but the file does not exist`);
      } else if (s.kind === 'retired') {
        console.error(`  ${s.path}: exempt at ${s.recorded} lines, now ${s.now} and compliant`);
      } else {
        console.error(`  ${s.path}: ceiling ${s.recorded} lines, now ${s.now}`);
      }
    }
    console.error(
      '\n  A stale exemption is a claim about the code that stopped being true,\n' +
        '  and it leaves headroom for the file to grow back. Run:\n' +
        '    npm run check:file-length:update\n' +
        '  and commit the smaller baseline.',
    );
  }

  if (failed) process.exit(1);

  const exemptCount = files.filter((f) => baseline.has(f.path)).length;
  const max = files.reduce((m, f) => Math.max(m, f.lines), 0);
  console.log(
    `file-length: ok. ${files.length} governed file(s), max ${max} code lines, ` +
      `${exemptCount} exempt above ${LIMIT}.`,
  );
}

const args = process.argv.slice(2);
if (args.includes('--update')) {
  update();
} else {
  check(args.includes('--list'));
}
