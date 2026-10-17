// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { Check, CheckAborted, defineCheck, resolveCheck, toPattern, formatDuration } from '../src/check';
import type { Diagnostics, Problem } from '../src/diagnostics';
import type { Page } from '@playwright/test';

/**
 * The collector is the part of the check contract an agent touches on every line,
 * so its tests pin what a report says, not only which branch ran. A label that
 * reads wrong in a report is a wrong report.
 */

/** A page that answers only what the assertions below read. */
function fakePage(options: { url?: string; title?: string; text?: string; count?: number } = {}): Page {
  return {
    url: () => options.url ?? 'http://app.localhost:8080/x',
    title: async () => options.title ?? 'App - Sign in',
    evaluate: async () => options.text ?? 'Sign in\nPassword',
    locator: () => ({
      count: async () => options.count ?? 0,
      first: () => ({ isVisible: async () => (options.count ?? 0) > 0 }),
    }),
  } as unknown as Page;
}

function fakeDiag(problems: Problem[] = []): Diagnostics {
  return {
    failures: (ignore?: (p: Problem) => boolean) => problems.filter((p) => !ignore?.(p)),
  } as unknown as Diagnostics;
}

function checkWith(options: { page?: Page; diag?: Diagnostics } = {}): Check {
  return new Check({
    name: 'a check',
    page: options.page ?? fakePage(),
    diag: options.diag ?? fakeDiag(),
  });
}

describe('Check assertions', () => {
  it('records a pass without a detail', () => {
    const check = checkWith();
    expect(check.ok(true, 'the app is running', 'this text is for a failure')).toBe(true);
    expect(check.results[0]).toMatchObject({ status: 'pass', label: 'the app is running', detail: '' });
  });

  it('records a failure with the reason it was given, or a plain default', () => {
    const check = checkWith();
    check.ok(false, 'the app converged', 'state: error');
    check.ok(false, 'the tile is gone');
    expect(check.results[0].detail).toBe('state: error');
    expect(check.results[1].detail).toBe('the condition was false');
  });

  it('compares objects by their content and prints both sides', () => {
    const check = checkWith();
    check.eq({ zone: 'UTC' }, { zone: 'Europe/Berlin' }, 'the clock matches');
    expect(check.results[0].status).toBe('fail');
    expect(check.results[0].detail).toContain('expected {"zone":"Europe/Berlin"}');
    expect(check.results[0].detail).toContain('got {"zone":"UTC"}');
    check.eq({ zone: 'UTC' }, { zone: 'UTC' }, 'the clock matches');
    expect(check.results[1].status).toBe('pass');
  });

  it('matches a pattern and a plain substring the same way', () => {
    const check = checkWith();
    check.match('Jellyfin - Media Server', /jellyfin/i, 'the title names the app');
    check.match('Jellyfin - Media Server', 'Media Server', 'the title names the kind');
    check.match('Jellyfin - Media Server', 'Plex', 'the title names Plex');
    expect(check.results.map((r) => r.status)).toEqual(['pass', 'pass', 'fail']);
    expect(check.results[2].detail).toContain('expected "Plex"');
  });

  it('treats a skip as neither a pass nor a failure', () => {
    const check = checkWith();
    check.skip('the LDAP form', 'the app declares native-oidc');
    expect(check).toMatchObject({ passed: 0, failed: 0, skipped: 1, allPassed: true });
  });

  it('calls a run that proved nothing not a pass', () => {
    expect(checkWith().allPassed).toBe(false);
    expect(checkWith().summary()).toContain('no assertions ran');
  });
});

describe('Check page assertions', () => {
  it('reads the title and the url through the page', async () => {
    const check = checkWith();
    await expect(check.titleIs(/Sign in/, 'the sign-in form is up')).resolves.toBe(true);
    await expect(check.urlIs('app.localhost', 'still on the app origin')).resolves.toBe(true);
    expect(check.results.map((r) => r.status)).toEqual(['pass', 'pass']);
  });

  it('waits for a selector to appear and reports the wait it cost', async () => {
    const found = checkWith({ page: fakePage({ count: 3 }) });
    await expect(found.appears('.app-slot', 'the grid paints', 1_000)).resolves.toBe(true);
    expect(found.results[0].status).toBe('pass');

    const missing = checkWith({ page: fakePage({ count: 0 }) });
    await expect(missing.appears('.app-slot', 'the grid paints', 300)).resolves.toBe(false);
    expect(missing.results[0].detail).toContain('did not become true within 300ms');
  });

  it('counts only the problems that are not set aside', async () => {
    const problems: Problem[] = [
      { kind: 'http', url: 'http://x/api/auth/me', detail: '401 on GET' },
      { kind: 'pageerror', detail: 'boom' },
    ];
    const check = checkWith({ diag: fakeDiag(problems) });
    await expect(check.noProblems()).resolves.toBe(false);
    expect(check.results[0].detail).toContain('PAGEERROR boom');

    const clean = checkWith({ diag: fakeDiag([problems[0]]) });
    await expect(clean.noProblemsIgnoringSignIn()).resolves.toBe(true);
    // What was set aside is printed, never dropped in silence.
    expect(clean.notes[0].message).toContain('ignored 1 sign-in round-trip problem');
  });
});

describe('Check.must', () => {
  it('stops the check when the step fails, and records why', async () => {
    const check = checkWith();
    await expect(check.must('the install converges', () => false)).rejects.toBeInstanceOf(CheckAborted);
    expect(check.results).toHaveLength(1);
    expect(check.results[0]).toMatchObject({ status: 'fail', label: 'the install converges' });
  });

  it('records the thrown message and stops', async () => {
    const check = checkWith();
    await expect(check.must('the address answers', () => Promise.reject(new Error('ECONNREFUSED')))).rejects.toBeInstanceOf(CheckAborted);
    expect(check.results[0].detail).toBe('ECONNREFUSED');
  });

  it('returns the value and records the pass when the step succeeds', async () => {
    const check = checkWith();
    await expect(check.must('the catalog loads', () => ['jellyfin'])).resolves.toEqual(['jellyfin']);
    expect(check.results[0].status).toBe('pass');
  });
});

describe('the report', () => {
  it('prints one line per result, its detail indented, and a summary', () => {
    const check = checkWith({ page: fakePage() });
    check.ok(true, 'the app is running');
    check.fail('the page loaded clean', '2 problem(s):\nHTTP 401 on GET http://x\nCONSOLE boom');
    check.skip('the LDAP form', 'the app declares native-oidc');
    const lines = check.format().split('\n');
    expect(lines[0]).toBe('a check');
    expect(lines[1]).toMatch(/^ {2}PASS the app is running {2}\(.+\)$/);
    expect(lines).toContain('        2 problem(s):');
    expect(lines).toContain('        HTTP 401 on GET http://x');
    expect(lines.at(-1)).toMatch(/^3 checks: 1 passed, 1 failed, 1 skipped in .+$/);
  });

  it('names the counts it has, and omits the ones it does not', () => {
    const check = checkWith();
    check.ok(true, 'one');
    check.ok(true, 'two');
    expect(check.summary()).toMatch(/^2 checks: 2 passed in .+$/);
  });

  it('carries the same facts as data', () => {
    const check = checkWith();
    check.ok(true, 'one');
    const data = check.json();
    expect(data).toMatchObject({ name: 'a check', ok: true, passed: 1, failed: 0, skipped: 0 });
    expect(data.results[0].label).toBe('one');
  });
});

describe('the check module contract', () => {
  it('accepts a bare default function and names it after the file', () => {
    const resolved = resolveCheck({ default: () => {} }, 'my-check.mjs');
    expect(resolved).toMatchObject({ name: 'my-check.mjs', options: {} });
  });

  it('takes the name and options a module declares', () => {
    const resolved = resolveCheck(
      { default: () => {}, name: 'app health', options: { target: 'jellyfin' } },
      'file.mjs',
    );
    expect(resolved).toMatchObject({ name: 'app health', options: { target: 'jellyfin' } });
  });

  it('accepts the defineCheck shape', () => {
    const resolved = resolveCheck({ default: defineCheck('round trip', () => {}) }, 'file.mjs');
    expect(resolved.name).toBe('round trip');
  });

  it('names what a wrong file exported instead of failing inside the run', () => {
    expect(() => resolveCheck({ helper: 1, default: 42 }, 'oops.mjs')).toThrow(/must default-export a function/);
    expect(() => resolveCheck({ helper: 1, default: 42 }, 'oops.mjs')).toThrow(/exports: helper, default/);
  });
});

describe('patterns and durations', () => {
  it('reads a slash pattern as a regular expression and anything else as a substring', () => {
    expect(toPattern('/jellyfin/i')).toBeInstanceOf(RegExp);
    expect(toPattern('502')).toBe('502');
    expect(toPattern('/unclosed')).toBe('/unclosed');
    // A path is not a regex with the flag `w`.
    expect(toPattern('/if/flow')).toBe('/if/flow');
    expect(toPattern('/if/flow/i')).toBeInstanceOf(RegExp);
  });

  it('prints a duration a person can read', () => {
    expect(formatDuration(812)).toBe('812ms');
    expect(formatDuration(4_200)).toBe('4.2s');
    expect(formatDuration(62_000)).toBe('1m02s');
  });
});
