// SPDX-License-Identifier: AGPL-3.0-only
//
// The check contract: what an ad-hoc validation script exports, and the
// assertions it gets from the runner.
//
// A check is one small file. It default-exports a function, and the runner hands
// that function everything it needs: a page, the host-agent API client, the page
// diagnostics, and a collector. The collector records every result instead of
// throwing on the first, so one run answers every question the check asks.
import { pathToFileURL } from 'node:url';
import type { Locator, Page } from '@playwright/test';
import * as api from './api';
import type { Diagnostics, Problem } from './diagnostics';
import { isAuthRoundTripNoise, visibleText } from './diagnostics';
import { appOrigin } from './origin';
import type { HarnessSession } from './session';
import { waitFor } from './wait';

/** The three outcomes one assertion can record. */
export type CheckStatus = 'pass' | 'fail' | 'skip';

/** One recorded assertion. */
export interface CheckResult {
  status: CheckStatus;
  label: string;
  detail: string;
  /** Wall time since the previous result, so a slow step names itself. */
  ms: number;
}

/** One recorded line of evidence. */
export interface CheckNote {
  message: string;
  ms: number;
}

/** Raised by `Check.must` to stop a check that cannot continue. */
export class CheckAborted extends Error {}

/** Render a value inside an assertion message: short, and quoted when it is text. */
function show(value: unknown): string {
  if (typeof value === 'string') {
    const text = value.length > 160 ? `${value.slice(0, 160)}...` : value;
    return JSON.stringify(text);
  }
  if (value instanceof RegExp) return String(value);
  if (value === undefined) return 'undefined';
  if (value === null) return 'null';
  if (typeof value === 'object') {
    const json = JSON.stringify(value) ?? String(value);
    return json.length > 160 ? `${json.slice(0, 160)}...` : json;
  }
  return String(value);
}

/** A duration a person can read: 812ms, 4.2s, 1m02s. */
export function formatDuration(ms: number): string {
  if (ms < 1_000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1_000).toFixed(1)}s`;
  const minutes = Math.floor(ms / 60_000);
  const seconds = Math.round((ms % 60_000) / 1_000);
  return `${minutes}m${String(seconds).padStart(2, '0')}s`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Read a pattern a person typed on a command line.
 *
 * `/source/flags` is a regular expression; anything else is a literal substring.
 * Nobody should have to escape a dot to ask whether a page says "502". Only the
 * flags JavaScript knows count as a pattern, so `/if/flow` stays the path it
 * looks like rather than the regex `if/flo` with the flag `w`. This mirrors the
 * rule in `cli/args.mjs`, which is what validates the same string on the way in.
 */
export function toPattern(value: string): RegExp | string {
  if (!value.startsWith('/')) return value;
  const last = value.lastIndexOf('/');
  if (last < 1) return value;
  const flags = value.slice(last + 1);
  if (!/^[dgimsuvy]*$/.test(flags)) return value;
  try {
    return new RegExp(value.slice(1, last), flags);
  } catch {
    return value;
  }
}

/** What the assertions can look at: the page, and what it recorded. */
export interface CheckEnv {
  name: string;
  page: Page;
  diag: Diagnostics;
  /** Called for each note, so a runner can stream progress as it happens. */
  onNote?: (message: string) => void;
}

/**
 * The collector one check writes into.
 *
 * Every method records a result and returns a boolean, so a check can branch on
 * an assertion without a second read. Nothing here throws on a failed assertion:
 * a check that stops at the first failure tells the reader one thing about a
 * page they wanted to know six things about. `must` is the exception, and it is
 * the tool for the one case where continuing produces noise instead of evidence.
 */
export class Check {
  readonly results: CheckResult[] = [];
  readonly notes: CheckNote[] = [];
  private readonly startedAt = Date.now();
  private lastAt = Date.now();

  constructor(private readonly env: CheckEnv) {}

  get name(): string {
    return this.env.name;
  }

  private record(status: CheckStatus, label: string, detail = ''): void {
    const now = Date.now();
    this.results.push({ status, label, detail, ms: now - this.lastAt });
    this.lastAt = now;
  }

  /**
   * Record `condition` as a pass or a failure, and return it.
   *
   * `detail` explains a failure, so a pass does not carry it.
   */
  ok(condition: unknown, label: string, detail = ''): boolean {
    const passed = Boolean(condition);
    this.record(passed ? 'pass' : 'fail', label, passed ? '' : detail || 'the condition was false');
    return passed;
  }

  /** Record a failure the check decided on its own, without an assertion. */
  fail(label: string, detail = ''): false {
    this.record('fail', label, detail);
    return false;
  }

  /** Record that this run does not apply. A skip is not a pass and not a failure. */
  skip(label: string, reason = ''): false {
    this.record('skip', label, reason);
    return false;
  }

  /** Print a line of evidence: what the check saw, or what it is waiting on. */
  note(message: string): void {
    const now = Date.now();
    this.notes.push({ message, ms: now - this.lastAt });
    this.lastAt = now;
    this.env.onNote?.(message);
  }

  /** Assert two values are equal. Objects and arrays compare by their JSON. */
  eq(actual: unknown, expected: unknown, label: string): boolean {
    const same = JSON.stringify(actual) === JSON.stringify(expected);
    return this.ok(same, label, same ? '' : `expected ${show(expected)}, got ${show(actual)}`);
  }

  /** Assert a string matches a pattern, or contains a literal substring. */
  match(value: string, pattern: RegExp | string, label: string): boolean {
    const found = pattern instanceof RegExp ? pattern.test(value) : value.includes(pattern);
    return this.ok(found, label, found ? '' : `expected ${show(pattern)} in ${show(value)}`);
  }

  /** Assert a string contains a literal substring. */
  includes(haystack: string, needle: string, label: string): boolean {
    return this.match(haystack, needle, label);
  }

  /**
   * Assert the page recorded no failed request, console error, page error, or
   * dead socket. `ignore` is the caller's filter for noise it can name.
   */
  async noProblems(
    label = 'the page loaded without problems',
    ignore?: (problem: Problem) => boolean,
  ): Promise<boolean> {
    const failures = this.env.diag.failures(ignore);
    if (failures.length === 0) return this.ok(true, label);
    const list = failures
      .slice(0, 10)
      .map((p) => `${p.kind.toUpperCase()} ${p.detail}${p.url ? `  ${p.url}` : ''}`)
      .join('\n');
    return this.ok(false, label, `${failures.length} problem(s):\n${list}`);
  }

  /**
   * Assert no problems, except the ones a sign-in round trip explains, and report
   * what was set aside.
   *
   * This is the shape almost every Bloud page needs: an app that delegates
   * authentication answers 401 on its own session probe until the identity
   * provider has finished. See `isAuthRoundTripNoise`.
   */
  async noProblemsIgnoringSignIn(label = 'the page loaded without problems'): Promise<boolean> {
    const setAside: Problem[] = [];
    const clean = await this.noProblems(label, (problem) => {
      const noise = isAuthRoundTripNoise(problem);
      if (noise) setAside.push(problem);
      return noise;
    });
    if (setAside.length > 0) {
      this.note(
        `ignored ${setAside.length} sign-in round-trip problem(s): ` +
          setAside.map((p) => `${p.detail} ${p.url ?? ''}`.trim()).join('; '),
      );
    }
    return clean;
  }

  /** The page's visible text, read through open shadow roots. */
  text(): Promise<string> {
    return visibleText(this.env.page);
  }

  /** Assert the page's visible text matches a pattern. */
  async textIncludes(pattern: RegExp | string, label: string): Promise<boolean> {
    const text = await this.text().catch((err: unknown) => `(${errorMessage(err)})`);
    return this.match(text, pattern, label);
  }

  /** Assert the document title matches a pattern. */
  async titleIs(pattern: RegExp | string, label: string): Promise<boolean> {
    const title = await this.env.page.title().catch(() => '');
    return this.match(title, pattern, label);
  }

  /** Assert the page URL matches a pattern, or contains a substring. */
  async urlIs(pattern: RegExp | string, label: string): Promise<boolean> {
    return this.match(this.env.page.url(), pattern, label);
  }

  /** Assert a selector or locator is visible. */
  async visible(target: Locator | string, label: string): Promise<boolean> {
    const locator = typeof target === 'string' ? this.env.page.locator(target) : target;
    const shown = await locator.first().isVisible().catch(() => false);
    return this.ok(shown, label, shown ? '' : `expected ${show(target)} to be visible`);
  }

  /**
   * Wait for a condition, then record whether it arrived.
   *
   * A fixed sleep is the usual wrong answer after a navigation: it is either
   * too short on a slow app or a minute wasted on a fast one. This polls and
   * reports the wait it cost.
   */
  async poll(label: string, condition: () => boolean | Promise<boolean>, timeoutMs = 30_000): Promise<boolean> {
    try {
      await waitFor(label, async () => await condition(), timeoutMs);
      return this.ok(true, label);
    } catch (err) {
      return this.ok(false, label, errorMessage(err));
    }
  }

  /**
   * Wait for a selector to match at least one element, then record it.
   *
   * This is the poll most checks want, and writing it inline invites one mistake:
   * `() => page.locator(x).count() > 0` compares a promise with a number and is
   * always false. Awaiting inside this method is the caller's one chance to get
   * that wrong, so it is not the caller's job.
   */
  async appears(selector: string, label: string, timeoutMs = 30_000): Promise<boolean> {
    return this.poll(label, async () => (await this.env.page.locator(selector).count()) > 0, timeoutMs);
  }

  /**
   * Run a step that everything after it depends on, and stop the check if it fails.
   *
   * Use it for the step whose failure makes every later assertion noise: an
   * install that never converged, a page that never loaded. The failure is
   * recorded like any other, so the report still reads as one table.
   */
  async must<T>(label: string, step: () => T | false | Promise<T | false>): Promise<T> {
    let value: T | false;
    try {
      value = await step();
    } catch (err) {
      this.fail(label, errorMessage(err));
      throw new CheckAborted(label);
    }
    if (value === false) {
      this.fail(label, 'the step returned false');
      throw new CheckAborted(label);
    }
    this.ok(true, label);
    return value;
  }

  get passed(): number {
    return this.results.filter((r) => r.status === 'pass').length;
  }

  get failed(): number {
    return this.results.filter((r) => r.status === 'fail').length;
  }

  get skipped(): number {
    return this.results.filter((r) => r.status === 'skip').length;
  }

  /** True when nothing failed and at least one assertion ran. A run with no
   * results is a run that proved nothing, so it is not a pass. */
  get allPassed(): boolean {
    return this.failed === 0 && this.results.length > 0;
  }

  /** One line: what the run proved, and what cost. */
  summary(): string {
    const total = this.results.length;
    if (total === 0) return `${this.name}: no assertions ran`;
    const parts = [`${this.passed} passed`];
    if (this.failed) parts.push(`${this.failed} failed`);
    if (this.skipped) parts.push(`${this.skipped} skipped`);
    return `${total} check${total === 1 ? '' : 's'}: ${parts.join(', ')} in ${formatDuration(Date.now() - this.startedAt)}`;
  }

  /** The whole run as a table, newest last. Notes are not repeated here: the
   * runner prints each one as it happens, before this table. */
  format(): string {
    const lines = [this.name];
    for (const result of this.results) {
      const status = result.status.toUpperCase().padEnd(4);
      lines.push(`  ${status} ${result.label}  (${formatDuration(result.ms)})`);
      for (const line of result.detail.split('\n')) {
        if (line.trim()) lines.push(`        ${line}`);
      }
    }
    lines.push(this.summary());
    return lines.join('\n');
  }

  /** The whole run as data, for a caller that parses rather than reads. */
  json(): CheckReportData {
    return {
      name: this.name,
      ok: this.allPassed,
      passed: this.passed,
      failed: this.failed,
      skipped: this.skipped,
      ms: Date.now() - this.startedAt,
      results: [...this.results],
      notes: [...this.notes],
    };
  }
}

/** The shape `Check.json` prints. */
export interface CheckReportData {
  name: string;
  ok: boolean;
  passed: number;
  failed: number;
  skipped: number;
  ms: number;
  results: CheckResult[];
  notes: CheckNote[];
}

/** Everything a check receives. A check file imports nothing to get it. */
export interface CheckContext {
  /** The assertions. Each one records a result and returns its boolean. */
  check: Check;
  /** The Playwright page, signed in unless the run passed `--anon`. */
  page: Page;
  /** The session that owns the page: `open`, `review`, `screenshot`, `diag`. */
  session: HarnessSession;
  /** What the page recorded: failed requests, console errors, page errors, sockets. */
  diag: Diagnostics;
  /** The host-agent API client. */
  api: typeof api;
  /** The whole harness library, for anything not lifted into this context. */
  harness: HarnessLibrary;
  /** The target the command named: `bloud`, an app id, or a url. */
  target: string;
  /** Extra positional arguments after the target, for a check of its own. */
  args: string[];
  /** Open a target, or the one the command named. */
  open(target?: string): Promise<void>;
  /** Capture a screenshot and return its path. */
  shot(name?: string): Promise<string>;
  /** Print a line of evidence as the run goes. */
  note(message: string): void;
  /** The origin an app's subdomain is served under. */
  origin(app: string): string;
  /**
   * A second page, in a browser context of its own.
   *
   * The run's own page keeps its sign-in. A check that needs an anonymous
   * look, or a second identity, asks for a page here instead of disturbing it.
   */
  newPage(options?: { signedIn?: boolean }): Promise<HarnessSession>;
}

/** The library surface, typed against the module that re-exports this file. */
export type HarnessLibrary = typeof import('./index');

/** The function a check file runs. */
export type CheckFn = (context: CheckContext) => void | Promise<void>;

/** Defaults a check file can declare for itself. The command line wins. */
export interface CheckModuleOptions {
  /** The target to open when the command named none. */
  target?: string;
  /** False when the check reads an anonymous page. */
  signedIn?: boolean;
  /** Wall-clock budget in milliseconds. */
  timeoutMs?: number;
  /** False when the check drives its own navigation and wants no page opened first. */
  open?: boolean;
}

/** A check file's export, after `resolveCheck` normalizes it. */
export interface CheckModule {
  name: string;
  run: CheckFn;
  options: CheckModuleOptions;
}

/**
 * Name a check and its function.
 *
 * A file may default-export a bare function instead; then the report is named
 * after the file. `defineCheck` is for the case where the run wants a name a
 * person chose.
 */
export function defineCheck(name: string, run: CheckFn): CheckModule;
export function defineCheck(module: CheckModule): CheckModule;
export function defineCheck(nameOrModule: string | CheckModule, run?: CheckFn): CheckModule {
  if (typeof nameOrModule === 'string') {
    if (!run) throw new Error(`defineCheck("${nameOrModule}", run) needs a function`);
    return { name: nameOrModule, run, options: {} };
  }
  return { ...nameOrModule, options: nameOrModule.options ?? {} };
}

/**
 * Read a check file's exports into one shape.
 *
 * A wrong shape is the most common mistake in a first check, so the message says
 * what the file exported instead of failing inside the run.
 */
export function resolveCheck(mod: Record<string, unknown>, fallbackName: string): CheckModule {
  const exported = mod.default;
  if (typeof exported === 'function') {
    const options = (mod.options as CheckModuleOptions | undefined) ?? {};
    const name = typeof mod.name === 'string' ? mod.name : fallbackName;
    return { name, run: exported as CheckFn, options };
  }
  if (exported && typeof exported === 'object' && typeof (exported as CheckModule).run === 'function') {
    return defineCheck(exported as CheckModule);
  }
  const keys = Object.keys(mod).join(', ') || 'nothing';
  throw new Error(
    `a check file must default-export a function or { name, run }. ` +
      `This file exports: ${keys}.`,
  );
}

/** What `runCheckFile` needs from the caller: a live session, and the run's arguments. */
export interface RunCheckOptions {
  session: HarnessSession;
  target: string;
  args?: string[];
  /** The library surface to hand the check as `harness`. */
  harness: HarnessLibrary;
  /** Called for each `note` and each result, so a runner can stream progress. */
  onEvent?: (line: string) => void;
}

/**
 * Load one check file and run it against a live session.
 *
 * The import is dynamic and by absolute URL, so the file can sit anywhere: a
 * scratch directory, the repo, or `harness/checks/`. A file that throws on
 * import fails the same way a check that fails does: one report, one reason.
 */
export async function runCheckFile(file: string, options: RunCheckOptions): Promise<Check> {
  const check = new Check({
    name: options.target,
    page: options.session.page,
    diag: options.session.diag,
    onNote: options.onEvent,
  });
  let mod: Record<string, unknown>;
  try {
    mod = (await import(pathToFileURL(file).href)) as Record<string, unknown>;
  } catch (err) {
    check.fail(`the check file loaded`, errorMessage(err));
    return check;
  }

  let resolved: CheckModule;
  try {
    resolved = resolveCheck(mod, file);
  } catch (err) {
    check.fail('the check file is a valid check', errorMessage(err));
    return check;
  }

  const named = new Check({
    name: resolved.name,
    page: options.session.page,
    diag: options.session.diag,
    onNote: options.onEvent,
  });
  const context = buildContext(named, options);
  try {
    await resolved.run(context);
  } catch (err) {
    if (!(err instanceof CheckAborted)) {
      named.fail('the check ran to the end', errorMessage(err));
    }
  }
  return named;
}

function buildContext(check: Check, options: RunCheckOptions): CheckContext {
  const { session } = options;
  return {
    check,
    page: session.page,
    session,
    diag: session.diag,
    api,
    harness: options.harness,
    target: options.target,
    args: options.args ?? [],
    open: (target?: string) => session.open(target ?? options.target),
    shot: (name?: string) => session.screenshot(name ?? options.target),
    note: (message: string) => check.note(message),
    origin: (app: string) => appOrigin(app),
    newPage: (pageOptions?: { signedIn?: boolean }) => session.fork(pageOptions),
  };
}
