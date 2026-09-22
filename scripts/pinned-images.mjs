// SPDX-License-Identifier: AGPL-3.0-only
//
// pinned-images: every container image Bloud runs must name a specific version.
//
//   node scripts/pinned-images.mjs           # check; exit 1 on a violation
//   node scripts/pinned-images.mjs --list    # also list every checked ref and its state
//
// Why: a rolling tag is republished upstream, so `:release` today and `:release`
// next month are different bytes. That makes a crash unreproducible, makes a
// version bump unreviewable, and means one bad upstream push lands on every Bloud
// install at once. Pinning is what turns "the app broke" into a diffable change.
//
// What counts as pinned: an explicit version tag in any registry's shape
// (`1.2.3`, `v3.4`, `7-alpine`, `pg16`, `2.6.5.5623-ls161`), or an
// `@sha256:` digest. What fails: no tag at all (the runtime reads that as
// `:latest`), `:latest`, and the rolling channel tags in FLOATING.
//
// Where it looks: the `containers[].image` entries of `apps/*/metadata.yaml`
// (the catalog the planner turns into container specs) and registry-qualified
// image literals in non-test Go under `apps/` and `services/` (the sharing
// sidecar images live in Go constants, not in the catalog).
//
// Exceptions are declared in EXCEPTIONS below, each with a reason. The check
// prints them on every run so a floating tag is never invisible, and it fails an
// exception that matches nothing, so the list cannot rot into a claim about code
// that no longer exists.

import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';

// Rolling channel tags: words a registry republishes rather than versions.
// Matched exactly, or as a channel prefix (`release-cuda`, `unstable-v1.103`),
// so a variant of a rolling channel is caught with the channel.
const FLOATING = [
  'latest',
  'stable',
  'unstable',
  'release',
  'staging',
  'edge',
  'nightly',
  'hourly',
  'weekly',
  'main',
  'master',
  'trunk',
  'head',
  'develop',
  'dev',
  'bleeding',
  'canary',
  'current',
  'prod',
  'production',
  'beta',
  'next',
];

// Deliberate exceptions. Matching is by image ref; `file` narrows it to one
// declaration site so an exception cannot silently cover a second copy of the
// same image elsewhere.
const EXCEPTIONS = [
  {
    ref: 'docker.io/tailscale/tailscale:stable',
    file: 'services/host-agent/internal/sharing/tailnet_node.go',
    reason:
      'Strategic: the tailnet node must track the Tailscale stable channel. A ' +
      'pinned client drifts from the coordination server and from the peers it ' +
      'tunnels to, and the failure shows up as broken transport rather than as ' +
      'a version mismatch. Every stable release is a supported one, and the node ' +
      'carries no version-sensitive contract of its own. Re-review this if the ' +
      'node ever starts pinning ACL or key material that a Tailscale upgrade ' +
      'could change.',
  },
];

// Registries the catalog actually pulls from. A ref that is not registry-qualified
// in a Go line is a string that merely looks like an image, so the qualifier is
// what keeps the Go scan from guessing.
const REGISTRY_LITERAL =
  /"((?:docker\.io|ghcr\.io|quay\.io|lscr\.io|gcr\.io|public\.ecr\.aws|registry\.k8s\.io)\/[^"\s]+)"/g;

const CATALOG_IMAGE = /^(\s*)image:\s*(\S+)\s*$/;
const CATALOG_NAME = /^\s*(?:-\s+)?name:\s*(\S+)\s*$/;

// classify returns null when the ref is pinned, otherwise the reason it is not.
function classify(ref) {
  // A digest is the strongest pin there is; the tag on such a ref is decoration.
  if (/@sha[25]?\d[256]?:[0-9a-f]{16,}$/i.test(ref) || /@sha256:[0-9a-f]{64}$/.test(ref)) {
    return null;
  }

  const name = ref.slice(ref.lastIndexOf('/') + 1);
  const colon = name.lastIndexOf(':');
  if (colon <= 0) {
    return {
      reason:
        'no tag at all: the runtime reads an untagged image as `:latest`, which ' +
        'is the exact thing this check exists to stop.',
      fix: `${ref}:<version>  (name the version you actually run)`,
    };
  }

  const tag = name.slice(colon + 1);
  const stem = ref.slice(0, -(tag.length + 1));
  if (tag === '') {
    return {
      reason: 'empty tag (`repo:`): the runtime falls back to `:latest`.',
      fix: `${stem}:<version>`,
    };
  }

  const lower = tag.toLowerCase();
  const channel = FLOATING.find(
    (w) => lower === w || lower.startsWith(`${w}-`) || lower.startsWith(`${w}_`),
  );
  if (!channel) return null;

  if (lower === 'latest') {
    return {
      reason:
        '`latest` moves under every install: upstream republishes it on each ' +
        "build, so today's crash and next week's crash are different binaries, " +
        'and one bad upstream push hits every Bloud instance at once.',
      fix: `${stem}:<version>  (pin the version you verified)`,
    };
  }

  return {
    reason:
      `"${tag}" is a rolling channel tag (channel: ${channel}): upstream ` +
      'republishes it, so the ref names no specific version. The install is not ' +
      'reproducible and a bump is not reviewable.',
    fix: `${stem}:<version>  (pin the version you verified)`,
  };
}

// catalogRefs reads apps/*/metadata.yaml and returns refs with their context.
function catalogRefs(file) {
  const out = [];
  const lines = readFileSync(file, 'utf8').split('\n');
  const appName = file.split('/')[1];
  let inContainers = false;
  let container = null;

  lines.forEach((raw, i) => {
    const line = raw.replace(/\t/g, '  ');
    if (/^\s*#/.test(line)) return;

    // A column-0 key opens or closes the containers block; everything else keeps
    // the current state.
    if (/^[A-Za-z_][\w.-]*:/.test(line)) {
      inContainers = line.startsWith('containers:');
      container = null;
      return;
    }
    if (!inContainers) return;

    const named = CATALOG_NAME.exec(line);
    if (named) {
      container = named[1];
      return;
    }
    const imaged = CATALOG_IMAGE.exec(line);
    if (imaged) {
      out.push({
        file,
        line: i + 1,
        ref: imaged[2],
        app: appName,
        container: container ?? '(container name not seen above this line)',
        source: 'catalog',
      });
    }
  });
  return out;
}

// goRefs reads a non-test Go file and returns registry-qualified image literals.
function goRefs(file) {
  const out = [];
  const lines = readFileSync(file, 'utf8').split('\n');

  lines.forEach((line, i) => {
    if (/^\s*\/\//.test(line)) return; // comments name images without using them
    if (!/[Ii]mage/.test(line)) return;

    REGISTRY_LITERAL.lastIndex = 0;
    let m;
    while ((m = REGISTRY_LITERAL.exec(line)) !== null) {
      out.push({
        file,
        line: i + 1,
        ref: m[1],
        app: file.split('/')[1],
        container: null,
        source: 'go',
      });
    }
  });
  return out;
}

function trackedFiles(...args) {
  return execFileSync('git', ['ls-files', ...args], { encoding: 'utf8' })
    .split('\n')
    .filter(Boolean);
}

function isTestdata(file) {
  return file.includes('/testdata/') || file.endsWith('.golden.yml');
}

function main() {
  const listMode = process.argv.includes('--list');

  const catalogFiles = trackedFiles('apps/*/metadata.yaml').filter((f) => !isTestdata(f));
  const goFiles = [...trackedFiles('apps/*.go'), ...trackedFiles('services/*.go')].filter(
    (f) => !f.endsWith('_test.go') && !isTestdata(f) && existsSync(f),
  );

  const refs = [
    ...catalogFiles.flatMap(catalogRefs),
    ...goFiles.flatMap(goRefs),
  ];

  const usedExceptions = new Map(); // exception -> [where it applied]
  const violations = [];
  const templated = [];

  for (const r of refs) {
    // A ref built at runtime (%s, {{var}}) cannot be judged from the source.
    if (/%[sdv]|{{/.test(r.ref)) {
      templated.push(r);
      continue;
    }

    const problem = classify(r.ref);
    if (!problem) continue;

    const exception = EXCEPTIONS.find(
      (e) => e.ref === r.ref && (!e.file || e.file === r.file),
    );
    if (exception) {
      if (!usedExceptions.has(exception)) usedExceptions.set(exception, []);
      usedExceptions.get(exception).push(r);
      continue;
    }
    violations.push({ ...r, ...problem });
  }

  const staleExceptions = EXCEPTIONS.filter((e) => !usedExceptions.has(e));

  // One stream, in report order: mixing stdout and stderr interleaves unpredictably
  // depending on how the caller pipes the check, and a failure report whose
  // explanation arrives before its failure is worse than no report.
  const out = [];

  if (listMode) {
    out.push('Checked refs:');
    for (const r of refs) {
      const bad = classify(r.ref);
      const exception = bad
        ? EXCEPTIONS.find((e) => e.ref === r.ref && (!e.file || e.file === r.file))
        : null;
      const state = templated.includes(r) ? 'skipped' : bad ? (exception ? 'exception' : 'UNPINNED') : 'pinned';
      out.push(`  ${state.padEnd(9)} ${r.file}:${r.line}  ${r.ref}`);
    }
    out.push('');
  }

  const where = (r) =>
    r.container ? `${r.app} / ${r.container}` : `${r.source} constant (${r.app})`;

  for (const v of violations) {
    out.push(`FAIL  ${v.file}:${v.line}  ${where(v)}`);
    out.push(`      ref:     ${v.ref}`);
    out.push(`      problem: ${v.reason}`);
    out.push(`      change:  ${v.fix}`);
    out.push('');
  }

  for (const e of staleExceptions) {
    out.push(`FAIL  exception matches nothing: ${e.ref}`);
    out.push(`      declared for: ${e.file ?? 'any file'}`);
    out.push(
      '      problem: that image ref is not declared anywhere this check reads, so ' +
        'the exception describes code that is gone or was renamed.',
    );
    out.push('      change:  delete the entry from EXCEPTIONS in scripts/pinned-images.mjs');
    out.push('');
  }

  if (usedExceptions.size > 0) {
    out.push(
      'Exceptions this check accepted (printed on every run, so a floating tag is ' +
        'never invisible):',
    );
    for (const [e, hits] of usedExceptions) {
      out.push(`  ${e.ref}`);
      for (const h of hits) out.push(`    ${h.file}:${h.line}`);
      out.push(`    why: ${e.reason}`);
    }
    out.push('');
  }

  if (templated.length > 0) {
    out.push(
      `Not judged: ${templated.length} templated image ref(s), built at runtime:`,
    );
    for (const t of templated) out.push(`  ${t.file}:${t.line}  ${t.ref}`);
    out.push('');
  }

  const summary = `image pins: ${refs.length} ref(s) checked (${catalogFiles.length} catalog files, ${goFiles.length} Go files)`;
  const failures = violations.length + staleExceptions.length;

  if (failures > 0) {
    out.push(
      `${summary} -- ${failures} problem(s): ${violations.length} unpinned ref(s), ` +
        `${staleExceptions.length} stale exception(s).`,
    );
    out.push('');
    out.push('To pin a ref, find the version the floating tag points at right now:');
    out.push('  podman pull --quiet <ref>');
    out.push(
      '  podman inspect <ref> --format \'{{ index .Config.Labels "org.opencontainers.image.version" }}\'',
    );
    out.push('  podman search --list-tags <repo> | tail -20');
    out.push('');
    out.push('Re-run: npm run check:image-pins');
    process.stderr.write(`${out.join('\n')}\n`);
    process.exit(1);
  }

  out.push(`${summary}, every ref pinned to a specific version`);
  if (usedExceptions.size > 0) {
    out.push(`  (${usedExceptions.size} declared exception(s), listed above)`);
  }
  process.stdout.write(`${out.join('\n')}\n`);
}

// Exported for direct testing; the guard keeps a plain `node scripts/...` run the
// only thing that executes the check.
export { classify, catalogRefs, goRefs, FLOATING, EXCEPTIONS };

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main();
}
