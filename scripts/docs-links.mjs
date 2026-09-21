// SPDX-License-Identifier: AGPL-3.0-only
//
// docs-links: fail when a relative link in a Markdown file is broken, either
// because the target file does not exist or because a `#fragment` names a
// heading the target no longer has. Vale cannot resolve links, and docs move
// between `plans/` and `plans/archive/`, so this is the check that keeps the
// cross-references honest.
//
//   node scripts/docs-links.mjs
//
// External URLs are not fetched (that would make the check network-dependent),
// and links inside fenced code blocks are samples, not references, so both are
// skipped. Destinations are found by scanning for `](` rather than by matching
// whole links, so a nested badge (`[![alt](img)](LICENSE)`) still has its target
// checked. A destination the scanner cannot read is reported rather than
// skipped, so a broken link cannot pass by being unparseable.
//
// Reference-style definitions (`[text]: path`) are not resolved: no tracked
// document uses them.

import { execFileSync } from 'node:child_process';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { dirname, normalize, resolve } from 'node:path';

const FENCE = /^\s*(?:```|~~~)/;
const HEADING = /^#{1,6}\s+(.*?)\s*#*\s*$/;
const EXTERNAL = /^[a-z][a-z0-9+.-]*:/i;

// destinationsOf returns the destination of every `](` on the line: a string
// (possibly empty, meaning "skip"), or null when the destination is unreadable.
function destinationsOf(line) {
  const found = [];
  let i = 0;
  while ((i = line.indexOf('](', i)) !== -1) {
    let j = i + 2;
    while (j < line.length && /\s/.test(line[j])) j += 1;

    if (line[j] === '<') {
      const end = line.indexOf('>', j + 1);
      if (end === -1) {
        found.push(null);
        i += 2;
        continue;
      }
      found.push(line.slice(j + 1, end));
      i = end + 1;
      continue;
    }

    const end = line.indexOf(')', j);
    if (end === -1) {
      found.push(null);
      i += 2;
      continue;
    }
    const segment = line.slice(j, end);
    if (segment === '') {
      found.push('');
    } else {
      // An unquoted destination may not contain spaces; a trailing "title" is
      // allowed after one.
      const match = /^(\S+)(?:\s+"[^"]*")?$/.exec(segment);
      found.push(match ? match[1] : null);
    }
    i = end + 1;
  }
  return found;
}

// GitHub's heading slug: lowercase, drop punctuation, spaces to hyphens, and a
// numeric suffix for repeated headings.
function slugify(text) {
  return text
    .replace(/`([^`]*)`/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[*_]/g, '')
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, '')
    .trim()
    .replace(/\s+/g, '-');
}

function anchorsOf(file) {
  const seen = new Map();
  const anchors = new Set();
  let fenced = false;
  for (const line of readFileSync(file, 'utf8').split('\n')) {
    if (FENCE.test(line)) {
      fenced = !fenced;
      continue;
    }
    if (fenced) continue;
    const m = HEADING.exec(line);
    if (!m) continue;
    const base = slugify(m[1]);
    const n = seen.get(base) ?? 0;
    seen.set(base, n + 1);
    anchors.add(n === 0 ? base : `${base}-${n}`);
  }
  return anchors;
}

const files = execFileSync('git', ['ls-files', '*.md'], { encoding: 'utf8' })
  .split('\n')
  .filter(Boolean);

const problems = [];

for (const file of files) {
  const lines = readFileSync(file, 'utf8').split('\n');
  let fenced = false;
  lines.forEach((line, i) => {
    if (FENCE.test(line)) {
      fenced = !fenced;
      return;
    }
    if (fenced) return;

    const where = `${file}:${i + 1}`;
    for (const target of destinationsOf(line)) {
      if (target === null) {
        problems.push(
          `${where}: unreadable link destination (a destination containing spaces needs ` +
            `angle brackets: [text](<my file.md>))`,
        );
        continue;
      }
      if (target === '' || target.startsWith('#')) {
        const fragment = target.slice(1);
        if (fragment && !anchorsOf(file).has(fragment)) {
          problems.push(`${where}: missing anchor '#${fragment}' in ${file}`);
        }
        continue;
      }
      if (EXTERNAL.test(target) || target.startsWith('//')) continue;

      const [path, fragment] = target.split('#');
      const resolved = normalize(resolve(dirname(file), path));
      if (!existsSync(resolved)) {
        problems.push(`${where}: missing target '${target}' (resolved: ${resolved})`);
        continue;
      }
      if (!fragment) continue;
      if (!resolved.endsWith('.md') || !statSync(resolved).isFile()) continue;
      if (!anchorsOf(resolved).has(fragment)) {
        const rel = resolved.startsWith(process.cwd()) ? resolved.slice(process.cwd().length + 1) : resolved;
        problems.push(`${where}: missing anchor '#${fragment}' in ${rel}`);
      }
    }
  });
}

if (problems.length > 0) {
  for (const p of problems) console.error(p);
  console.error(`\n${problems.length} broken relative link(s).`);
  process.exit(1);
}
console.log(`docs-links: ${files.length} Markdown files, all relative links resolve`);
