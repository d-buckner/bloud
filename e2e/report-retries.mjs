// SPDX-License-Identifier: AGPL-3.0-only
//
// Report the e2e tests that only passed on a retry.
//
// docs/plans/ci-flakiness-reduction.md guardrail 2 allows one automatic
// rerun on one condition: "if a retry is added, report it, so it cannot
// quietly become the norm." This is that report. The e2e workflows run it
// after Playwright and surface each retried spec as a warning annotation.
//
// It never fails the build. A retried pass is the outcome the retry exists
// to produce, so failing on it would be the same as not retrying at all.
// A spec that failed every attempt is not listed here either: the run is
// already red and Playwright's own output names it.
//
// Usage: node report-retries.mjs [path-to-report.json]

import { existsSync, readFileSync } from 'node:fs';

const reportPath =
  process.argv[2] ??
  process.env.BLOUD_E2E_JSON_REPORT ??
  'test-results/e2e-report.json';

// Every exit path is 0. This step reports; it must never be the reason a
// green run turns red, and a missing or unreadable report is not a test
// failure.
const done = (message) => {
  console.log(message);
  process.exit(0);
};

if (!existsSync(reportPath)) {
  done(`report-retries: no Playwright JSON report at ${reportPath}, nothing to report`);
}

let report;
try {
  report = JSON.parse(readFileSync(reportPath, 'utf8'));
} catch (err) {
  done(`report-retries: could not parse ${reportPath}: ${err.message}`);
}

// A spec counts as "retried and passed" when some project ran it more than
// once and the final attempt passed. Playwright nests specs under suites at
// whatever depth the describe() blocks go, so walk the whole tree instead of
// assuming a fixed shape.
const retried = [];

const collect = (suite) => {
  for (const spec of suite.specs ?? []) {
    const reran = (spec.tests ?? []).filter((test) => (test.results ?? []).length > 1);
    if (reran.length === 0) continue;
    const recovered = reran.every(
      (test) => test.results[test.results.length - 1].status === 'passed',
    );
    if (!recovered) continue;
    retried.push({
      file: spec.file ?? suite.title ?? '?',
      title: spec.title ?? '(untitled)',
      attempts: Math.max(...reran.map((test) => test.results.length)),
    });
  }
  for (const child of suite.suites ?? []) collect(child);
};

for (const suite of report.suites ?? []) collect(suite);

if (retried.length === 0) {
  done('report-retries: no test needed a retry');
}

const noun = retried.length === 1 ? 'test' : 'tests';
console.log(`\nreport-retries: ${retried.length} ${noun} failed once, then passed on the retry:`);
for (const spec of retried) {
  const line = `${spec.file} :: ${spec.title} (passed on attempt ${spec.attempts})`;
  console.log(`  - ${line}`);
  if (process.env.GITHUB_ACTIONS) {
    // % is the annotation format escape; a title containing it would
    // otherwise corrupt the message.
    const escaped = line.replace(/%/g, '%25');
    console.log(`::warning file=${spec.file}::flaky e2e test: ${escaped}`);
  }
}
console.log(
  '\nA retry is not a fix. Each spec above is still flaky and should be made\n' +
    'deterministic; see docs/plans/ci-flakiness-reduction.md.',
);
process.exit(0);
