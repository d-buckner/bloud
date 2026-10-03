// SPDX-License-Identifier: AGPL-3.0-only
//
// checks: the shared runner behind the pre-commit and pre-push hooks.
//
//   node scripts/checks.mjs commit   # hygiene over the staged files
//   node scripts/checks.mjs push     # hygiene over the pushed range + test/lint
//   node scripts/checks.mjs all      # every check over the whole tree
//
// Why two hooks instead of one big pre-commit. A commit is something you make
// hundreds of times a week and it should cost nothing; a push is something you
// do once per branch of work and it is the last cheap moment before other
// people see it. Putting the test suites in pre-commit charged the expensive
// tier at the frequent event: a markdown typo fix ran three golangci-lint
// passes, eslint and both test suites, ~30s to change a sentence.
//
//   commit  license header, gofmt, em dash, prose, doc links,   ~1-2s
//           file length ratchet
//   push    the above over the range, plus lint + tests          ~5-25s
//
// Both tiers run their checks concurrently: they are independent, so the wall
// clock is the slowest check rather than the sum.
//
// Neither hook is the authority. CI runs the full `./bloud validate --tier
// fast` plus integration and e2e on every push, so `git commit --no-verify`
// loses you a few seconds of feedback, not correctness. That is deliberate: a
// hook you cannot get around is a hook people disable globally, and then the
// hygiene layer is gone for everyone.
//
// Scope rule: a check narrows only when its failures live in the files you
// touched. `check:docs-links`, `check:image-pins`, and `check:file-length`
// scan the whole tree on purpose: a relative link is broken by the file you
// deleted, a pin exception goes stale when an app is removed rather than
// edited, and the file-length baseline has to be reconciled against every
// governed file or a deleted exempt file leaves a stale ceiling behind. All
// three cost well under a second, so narrowing them buys nothing.

import { spawn, spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import os from 'node:os';

const ZERO_SHA = /^0+$/;

// ---------------------------------------------------------------- file sets

function git(args) {
  const out = spawnSync('git', args, { encoding: 'utf8' });
  if (out.status !== 0) return '';
  return out.stdout;
}

function lines(text) {
  return text.split('\n').filter(Boolean);
}

// stagedFiles returns { existing, touched }: the paths a per-file check can read,
// and every path the commit touches including deletions and renames. The second
// set is what whole-tree checks key off, because a deleted file is exactly the
// thing that breaks a link elsewhere.
function stagedFiles() {
  const existing = lines(git(['diff', '--cached', '--name-only', '--diff-filter=ACMR']));
  const touched = lines(git(['diff', '--cached', '--name-only', '--diff-filter=ACMRD']));
  return { existing, touched };
}

// pushedFiles reads the ref pairs git hands a pre-push hook on stdin:
//   <local_ref> <local_sha> <remote_ref> <remote_sha>
// A zero remote sha means the branch does not exist remotely yet, so the range
// is everything since the merge base with the default branch.
function pushedFiles(remote) {
  const ranges = [];
  const stdin = readStdin();
  for (const line of lines(stdin)) {
    const [, localSha, , remoteSha] = line.split(/\s+/);
    if (!localSha) continue;
    if (ZERO_SHA.test(remoteSha)) {
      const base = defaultBase();
      if (base) ranges.push(`${base}...${localSha}`);
    } else {
      ranges.push(`${remoteSha}..${localSha}`);
    }
  }
  if (ranges.length === 0) {
    // No stdin: invoked by hand. Fall back to the upstream, then the base.
    const upstream = remote || '@{upstream}';
    const probe = spawnSync('git', ['rev-parse', '--verify', '--quiet', upstream], { encoding: 'utf8' });
    const base = probe.status === 0 ? upstream : defaultBase();
    if (!base) return { existing: [], touched: [] };
    ranges.push(`${base}...HEAD`);
  }
  const existing = new Set();
  const touched = new Set();
  for (const range of ranges) {
    for (const f of lines(git(['diff', '--name-only', '--diff-filter=ACMR', range]))) existing.add(f);
    for (const f of lines(git(['diff', '--name-only', '--diff-filter=ACMRD', range]))) touched.add(f);
  }
  return { existing: [...existing], touched: [...touched] };
}

function defaultBase() {
  for (const candidate of ['origin/main', 'origin/master', 'main', 'master']) {
    const probe = spawnSync('git', ['rev-parse', '--verify', '--quiet', candidate], { encoding: 'utf8' });
    if (probe.status === 0) return candidate.trim();
  }
  return '';
}

function readStdin() {
  // The hook's stdin is the ref list git wrote there. Reading fd 0 directly
  // avoids a `cat` process, and returns '' when stdin is a terminal or closed,
  // which is the hand-invoked case.
  try {
    return readFileSync(0, 'utf8');
  } catch {
    return '';
  }
}

function allTracked() {
  return lines(git(['ls-files']));
}

// ------------------------------------------------------------- classifiers

const isGoSource = (f) => f.endsWith('.go');
// go.mod / go.sum changes move the same lint and test checks as a .go change,
// but they are not Go source: handing one to gofmt is a parse error. Keep the
// two sets apart rather than filtering at each use site.
const isGoModule = (f) => f.endsWith('go.mod') || f.endsWith('go.sum');
const isGo = (f) => isGoSource(f) || isGoModule(f);
const isWeb = (f) => f.startsWith('services/host-agent/web/');
const isHostAgent = (f) => f.startsWith('services/host-agent/') && !isWeb(f);
const isApps = (f) => f.startsWith('apps/');
const isCli = (f) => f.startsWith('cli/');
const isMarkdown = (f) => f.endsWith('.md');
// What Vale can read. Mirrors the extension list the old lint:prose one-liner
// passed to `git ls-files`.
const isValeReadable = (f) => /\.(md|go|ts|js|svelte|ya?ml|css|html|sql)$/.test(f);

const any = (files, pred) => files.some(pred);

// ------------------------------------------------------------------ checks
//
// `run(files)` returns the npm script to invoke plus the file arguments to
// append, or null to skip. Returning null is how a check says "nothing you
// staged can fail this", which is what keeps a docs commit at one second.

const CHECKS = [
  {
    id: 'license:check',
    tiers: ['commit', 'push'],
    run: (files) => ({ script: 'license:check', args: files }),
  },
  {
    id: 'check:gofmt',
    tiers: ['commit', 'push'],
    run: (files) => {
      const go = files.filter(isGoSource);
      return go.length > 0 ? { script: 'check:gofmt', args: go } : null;
    },
  },
  {
    id: 'check:no-emdash',
    tiers: ['commit', 'push'],
    run: (files) => ({ script: 'check:no-emdash', args: files }),
  },
  {
    id: 'lint:prose',
    tiers: ['commit', 'push'],
    run: (files) => {
      const prose = files.filter(isValeReadable);
      return prose.length > 0 ? { script: 'lint:prose', args: prose } : null;
    },
  },
  {
    // Whole-tree on purpose: a rename breaks links in files nobody staged.
    id: 'check:docs-links',
    tiers: ['commit', 'push'],
    run: (_files, touched) =>
      any(touched, isMarkdown) ? { script: 'check:docs-links', args: [] } : null,
  },
  {
    // Whole-tree on purpose: the pin check also validates its own exception
    // table, which goes stale when an app is removed rather than edited.
    id: 'check:image-pins',
    tiers: ['push'],
    run: () => ({ script: 'check:image-pins', args: [] }),
  },
  {
    // Whole-tree on purpose: the ratchet baseline is reconciled against every
    // governed file, so an exempt file that was deleted or shrank has to be
    // noticed even though nobody staged it.
    id: 'check:file-length',
    tiers: ['commit', 'push'],
    run: () => ({ script: 'check:file-length', args: [] }),
  },
  {
    id: 'lint:go:apps',
    tiers: ['push'],
    run: (files) => (any(files, isApps) ? { script: 'lint:go:apps', args: [] } : null),
  },
  {
    id: 'test:go:apps',
    tiers: ['push'],
    run: (files) => (any(files, isApps) ? { script: 'test:go:apps', args: [] } : null),
  },
  {
    id: 'lint:go:host-agent',
    tiers: ['push'],
    run: (files) =>
      any(files, isHostAgent) ? { script: 'lint:go:host-agent', args: [] } : null,
  },
  {
    id: 'test:go',
    tiers: ['push'],
    run: (files) => (any(files, isHostAgent) ? { script: 'test:go', args: [] } : null),
  },
  {
    id: 'lint:go:cli',
    tiers: ['push'],
    run: (files) => (any(files, isCli) ? { script: 'lint:go:cli', args: [] } : null),
  },
  {
    id: 'lint:web',
    tiers: ['push'],
    run: (files) => (any(files, isWeb) ? { script: 'lint:web', args: [] } : null),
  },
  {
    id: 'test:ts',
    tiers: ['push'],
    run: (files) => (any(files, isWeb) ? { script: 'test:ts', args: [] } : null),
  },
];

// The full sweep: every check, whole tree, no narrowing. This is what CI
// mirrors and what `npm run test:precommit` gives you.
function fullSweep() {
  const all = allTracked();
  const goFiles = all.filter(isGoSource);
  const proseFiles = all.filter(isValeReadable);
  const specs = [];
  for (const check of CHECKS) {
    if (check.id === 'check:gofmt') specs.push({ id: check.id, script: 'check:gofmt', args: goFiles });
    else if (check.id === 'lint:prose') specs.push({ id: check.id, script: 'lint:prose', args: proseFiles });
    else if (check.id === 'check:docs-links' || check.id === 'check:image-pins') {
      specs.push({ id: check.id, script: check.id, args: [] });
    } else {
      specs.push({ id: check.id, script: check.id, args: [] });
    }
  }
  return specs;
}

function select(mode, files, touched) {
  if (mode === 'all') return fullSweep();
  const specs = [];
  for (const check of CHECKS) {
    if (!check.tiers.includes(mode)) continue;
    const spec = check.run(files, touched);
    if (spec) specs.push({ id: check.id, ...spec });
  }
  return specs;
}

// ------------------------------------------------------------------ runner

const jobs = Math.max(1, Number(process.env.BLOUD_CHECK_JOBS || os.cpus().length));

function run(spec) {
  return new Promise((resolve) => {
    const started = Date.now();
    const child = spawn('npm', ['run', '--silent', spec.script, '--', ...spec.args], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let out = '';
    child.stdout.on('data', (c) => { out += c; });
    child.stderr.on('data', (c) => { out += c; });
    child.on('error', (err) => {
      out += `\nfailed to spawn: ${err.message}\n`;
      resolve({ ...spec, code: 127, seconds: (Date.now() - started) / 1000, out });
    });
    child.on('close', (code) => {
      resolve({ ...spec, code, seconds: (Date.now() - started) / 1000, out });
    });
  });
}

async function pool(specs, limit) {
  const results = [];
  const queue = [...specs];
  const workers = Array.from({ length: Math.min(limit, queue.length) }, async () => {
    while (queue.length > 0) {
      const spec = queue.shift();
      if (!spec) break;
      const result = await run(spec);
      results.push(result);
      const verdict = result.code === 0 ? 'ok' : `FAILED (exit ${result.code})`;
      console.log(`  ${spec.id.padEnd(22)} ${result.seconds.toFixed(1)}s  ${verdict}`);
    }
  });
  await Promise.all(workers);
  return results;
}

// -------------------------------------------------------------------- main

const mode = process.argv[2] || 'commit';
if (!['commit', 'push', 'all'].includes(mode)) {
  console.error(`usage: node scripts/checks.mjs <commit|push|all>`);
  process.exit(2);
}

let files;
let touched;
if (mode === 'all') {
  files = allTracked();
  touched = files;
} else if (mode === 'commit') {
  ({ existing: files, touched } = stagedFiles());
} else {
  ({ existing: files, touched } = pushedFiles(process.env.BLOUD_PUSH_REMOTE));
}

if (mode !== 'all' && files.length === 0 && touched.length === 0) {
  console.log(`checks: nothing ${mode === 'commit' ? 'staged' : 'to push'}, skipping`);
  process.exit(0);
}

const specs = select(mode, files, touched);
if (specs.length === 0) {
  console.log(`checks: no check applies to these ${files.length} file(s)`);
  process.exit(0);
}

const scopeNote = mode === 'all'
  ? 'whole tree'
  : `${files.length} file(s) ${mode === 'commit' ? 'staged' : 'being pushed'}`;
console.log(`checks (${mode}): ${specs.length} check(s), ${scopeNote}, ${jobs} concurrent`);
console.log('  ' + '-'.repeat(56));

const t0 = Date.now();
const results = await pool(specs, jobs);
const elapsed = (Date.now() - t0) / 1000;
console.log('  ' + '-'.repeat(56));

const failed = results.filter((r) => r.code !== 0).sort((a, b) => (a.id < b.id ? -1 : 1));
for (const result of failed) {
  console.error(`\n=== ${result.id} failed (exit ${result.code}) ===`);
  console.error(result.out.trimEnd());
}

if (failed.length > 0) {
  const names = failed.map((r) => r.id).join(', ');
  console.error(`\n${failed.length} of ${specs.length} check(s) failed in ${elapsed.toFixed(1)}s: ${names}`);
  console.error(
    '\nCI runs the full tier plus integration and e2e on every push, so this is\n' +
      'early feedback, not the gate. To commit anyway: git commit --no-verify\n' +
      'Re-run just what failed: npm run <check> -- <your files>',
  );
  process.exit(1);
}

console.log(`checks (${mode}): all ${specs.length} check(s) passed in ${elapsed.toFixed(1)}s`);
