// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { formatApiResponse, formatAppTable, formatStatus } from '../src/report';
import type { InstalledApp, StatusSnapshot } from '../src/api';

/**
 * These strings are what an operator or an agent reads after a run, so the tests
 * pin the wording where the wording carries the meaning: which app is not running,
 * and what the orchestrator said about it.
 */

function app(overrides: Partial<InstalledApp> & { catalog_id: string }): InstalledApp {
  return {
    display_name: overrides.catalog_id,
    status: 'running',
    is_system: false,
    ...overrides,
  } as InstalledApp;
}

describe('formatAppTable', () => {
  it('names each app, its state, its port, and nothing else when it is well', () => {
    const table = formatAppTable([app({ catalog_id: 'jellyfin', status: 'running', port: 8096 })]);
    expect(table).toContain('APP');
    expect(table).toMatch(/jellyfin\s+running\s+:8096\s*$/m);
  });

  it('prints the recorded error for an app that is not running', () => {
    const table = formatAppTable([
      app({ catalog_id: 'immich', status: 'error', last_error: 'postgres never became healthy' }),
    ]);
    expect(table).toContain('immich');
    expect(table).toContain('postgres never became healthy');
  });

  it('prints the live operation for an app that is still working', () => {
    const table = formatAppTable([
      app({
        catalog_id: 'paperless-ngx',
        status: 'installing',
        operation: { node: 'paperless-ngx', type: 'install', phase: 'EnsureContainer', status: 'running' },
      }),
    ]);
    expect(table).toContain('install/EnsureContainer running');
  });

  it('keeps a stale failed operation off the back of an app that came back', () => {
    const table = formatAppTable([
      app({
        catalog_id: 'hermes',
        status: 'running',
        operation: {
          node: 'hermes',
          type: 'install',
          phase: 'planning',
          status: 'failed',
          cause: 'interrupted by host-agent restart',
        },
      }),
    ]);
    expect(table).not.toContain('failed');
  });

  it('says so when there is nothing installed, and says so when the read failed', () => {
    expect(formatAppTable([])).toContain('none installed');
    expect(formatAppTable({ error: 'GET /api/apps/installed → 401' })).toContain('unreadable');
  });
});

describe('formatStatus', () => {
  function snapshot(overrides: Partial<StatusSnapshot> = {}): StatusSnapshot {
    return {
      health: { status: 'ok' },
      setup: { setupRequired: false, authentikReady: true, authReady: true },
      publicUrl: 'https://bloud.example.com',
      timezone: 'Europe/Berlin',
      transcription: { configured: false, name: '', baseUrl: '', model: '', hasApiKey: false, servedTo: [] },
      diagnostics: { skipped: false, hostResolves: true, containerChecked: true, containerResolves: true },
      apps: [app({ catalog_id: 'jellyfin' })],
      ...overrides,
    };
  }

  it('answers the four questions an operator asks first', () => {
    const status = formatStatus(snapshot());
    expect(status).toContain('health         ok');
    expect(status).toContain('provisioned, identity provider ready, auth ready');
    expect(status).toContain('https://bloud.example.com');
    expect(status).toContain('Europe/Berlin');
    expect(status).toContain('1 installed, 1 running');
  });

  it('says out loud when the identity provider is not there', () => {
    const status = formatStatus(
      snapshot({ setup: { setupRequired: false, authentikReady: false, authReady: true } }),
    );
    expect(status).toContain('identity provider NOT ready');
  });

  it('calls an unset clock what it means', () => {
    expect(formatStatus(snapshot({ timezone: '' }))).toContain('containers keep their own default');
  });

  it('reports a DNS divergence in the API own words', () => {
    const status = formatStatus(
      snapshot({
        diagnostics: { diverged: true, detail: 'the container resolver captured a stale upstream' },
      }),
    );
    expect(status).toContain('DIVERGED: the container resolver captured a stale upstream');
  });

  it('keeps going when one read failed, and prints which one', () => {
    const status = formatStatus(snapshot({ publicUrl: { error: 'GET /api/settings/public-url → 403' } }));
    expect(status).toContain('public URL     unreadable: GET /api/settings/public-url → 403');
    expect(status).toContain('health         ok');
  });
});

describe('formatApiResponse', () => {
  it('re-indents JSON and passes anything else through', () => {
    const pretty = formatApiResponse({
      method: 'GET',
      path: '/api/health',
      status: 200,
      ok: true,
      body: '{"status":"ok"}',
    });
    expect(pretty).toContain('GET /api/health -> 200');
    expect(pretty).toContain('"status": "ok"');

    const plain = formatApiResponse({
      method: 'GET',
      path: '/healthz',
      status: 200,
      ok: true,
      body: 'alive',
    });
    expect(plain).toBe('GET /healthz -> 200\nalive');
  });

  it('prints a status line even when the body is empty', () => {
    expect(formatApiResponse({ method: 'DELETE', path: '/x', status: 204, ok: true, body: '' })).toBe(
      'DELETE /x -> 204',
    );
  });
});
