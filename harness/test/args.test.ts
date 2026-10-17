// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { parseArgv } from '../cli/args.mjs';

/**
 * The command line is the whole interface, so every rejection has to name the
 * option that caused it: an agent reads one line, fixes the command, and reruns.
 */

describe('the target', () => {
  it('defaults to the dashboard and takes an app id or a url', () => {
    expect(parseArgv(['review'])).toMatchObject({ recipe: 'review', target: 'bloud', targetGiven: false });
    expect(parseArgv(['verify', 'jellyfin'])).toMatchObject({ target: 'jellyfin', targetGiven: true });
    expect(parseArgv(['review', 'https://example.test/x'])).toMatchObject({ target: 'https://example.test/x' });
  });

  it('refuses a second target rather than guessing which one was meant', () => {
    expect(parseArgv(['verify', 'jellyfin', 'immich']).error).toMatch(/unexpected extra argument "immich"/);
  });

  it('refuses a recipe it does not have, and prints the usage', () => {
    expect(parseArgv(['poke', 'jellyfin']).error).toMatch(/unknown recipe "poke"/);
  });
});

describe('the expect options', () => {
  it('collects each pattern with the kind it applies to', () => {
    const parsed = parseArgv([
      'verify',
      'jellyfin',
      '--expect-text',
      'Sign in',
      '--expect-text',
      '/password/i',
      '--expect-title',
      'Jellyfin',
      '--expect-url',
      '/if/flow',
    ]);
    expect(parsed.expects).toEqual([
      { kind: 'text', pattern: 'Sign in' },
      { kind: 'text', pattern: '/password/i' },
      { kind: 'title', pattern: 'Jellyfin' },
      { kind: 'url', pattern: '/if/flow' },
    ]);
  });

  it('rejects a pattern whose body cannot compile, naming it', () => {
    expect(parseArgv(['review', '--expect-text', '/(/i']).error).toMatch(/not a valid regular expression/);
  });

  it('keeps a path that starts with a slash a plain substring', () => {
    // `/if/flow` is what a person means: the Authentik path, not the regex
    // `if/flo` with the flag `w`.
    const parsed = parseArgv(['review', '--expect-url', '/if/flow']);
    expect(parsed.error).toBeUndefined();
    expect(parsed.expects).toEqual([{ kind: 'url', pattern: '/if/flow' }]);
  });
});

describe('the run recipe', () => {
  it('splits the check, the target, and the arguments after it', () => {
    const parsed = parseArgv(['run', 'app-health', 'jellyfin', '--json']);
    expect(parsed).toMatchObject({ recipe: 'run', script: 'app-health', target: 'jellyfin', targetGiven: true });
  });

  it('leaves the target unset when the caller named none, so the check can name its own', () => {
    expect(parseArgv(['run', 'api-smoke'])).toMatchObject({ script: 'api-smoke', targetGiven: false });
  });

  it('needs a check name or path', () => {
    expect(parseArgv(['run']).error).toMatch(/needs a check name or path/);
  });
});

describe('the api recipe', () => {
  it('takes a method, a path, and an optional body', () => {
    const parsed = parseArgv(['api', 'POST', '/api/apps/jellyfin/install', '{}']);
    expect(parsed.api).toEqual({ method: 'POST', path: '/api/apps/jellyfin/install', body: '{}' });
    expect(parseArgv(['api', '/api/health']).api).toMatchObject({ method: 'GET', path: '/api/health' });
  });

  it('needs a path', () => {
    expect(parseArgv(['api']).error).toMatch(/needs a path/);
  });
});

describe('options', () => {
  it('reads the flags', () => {
    const parsed = parseArgv(['shot', 'bloud', '--anon', '--full', '--json']);
    expect(parsed.options).toMatchObject({ anon: true, full: true, json: true });
  });

  it('reads the timed options as numbers', () => {
    const parsed = parseArgv(['review', '--settle', '250', '--timeout', '60000']);
    expect(parsed.options).toMatchObject({ settleMs: 250, timeoutMs: 60000 });
  });

  it('refuses a timed option with no number in front of it', () => {
    expect(parseArgv(['review', '--settle', 'soon']).error).toMatch(/number of milliseconds/);
    expect(parseArgv(['review', '--settle']).error).toMatch(/--settle needs a value/);
  });

  it('refuses an option it does not have', () => {
    expect(parseArgv(['review', '--turbo']).error).toMatch(/unknown option "--turbo"/);
  });
});

describe('help', () => {
  it('prints the usage for no arguments and for -h', () => {
    expect(parseArgv([]).help).toBe(true);
    expect(parseArgv(['-h']).help).toBe(true);
    expect(parseArgv(['--help']).help).toBe(true);
  });
});
