// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
//
// API calls go directly to the host-agent. Being loopback/trusted is a *scope*,
// not a credential: admin calls must present the runtime's API token, which the
// secrets manager writes next to secrets.json (host-agent-api-token).
import { execFile } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { promisify } from 'node:util';

const run = promisify(execFile);

function delay(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, ms);
  return promise;
}

const BASE_URL = process.env.BLOUD_API_URL ?? 'http://localhost:3000';

/**
 * Resolves the host-agent API token, in order of cost:
 *   1. BLOUD_API_TOKEN, exported by `./bloud e2e …` and `./bloud e2e app`.
 *   2. the runtime data dir, when a runner passed BLOUD_E2E_RUNTIME_DIR.
 *   3. `./bloud token`, which reads it through the backend (dev VMs).
 * Cached: the token does not change for the lifetime of a run.
 */
let tokenPromise: Promise<string> | undefined;
function apiToken(): Promise<string> {
  tokenPromise ??= (async () => {
    if (process.env.BLOUD_API_TOKEN) return process.env.BLOUD_API_TOKEN;

    const runtimeDir = process.env.BLOUD_E2E_RUNTIME_DIR;
    if (runtimeDir) {
      return (await readFile(join(runtimeDir, 'data', 'host-agent-api-token'), 'utf8')).trim();
    }

    const { stdout } = await run('../bloud', ['token']);
    const token = stdout.trim();
    if (!token) throw new Error('./bloud token produced no output');
    return token;
  })().catch((err: unknown) => {
    tokenPromise = undefined; // let a later call retry with a clearer failure
    throw new Error(
      `host-agent API credential unavailable (set BLOUD_API_TOKEN, BLOUD_E2E_RUNTIME_DIR, ` +
        `or run ./bloud token): ${err instanceof Error ? err.message : String(err)}`,
    );
  });
  return tokenPromise;
}

interface InstalledApp {
  catalog_id: string;
  status: string;
  /** Set by the orchestrator when the app hits a terminal error. */
  last_error?: string;
  display_name: string;
  is_system: boolean;
}

async function getApp(name: string): Promise<InstalledApp | null> {
  const body = await fetchJSON<{ apps: InstalledApp[] }>('/api/apps/installed');
  return body.apps.find((a) => a.catalog_id === name) ?? null;
}

async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${await apiToken()}`,
      ...init?.headers,
    },
  });
  if (!resp.ok) {
    throw new Error(`${init?.method ?? 'GET'} ${path} → ${resp.status}`);
  }
  return resp.json() as Promise<T>;
}

export async function getAppStatus(name: string): Promise<string | null> {
  const app = await getApp(name);
  return app?.status ?? null;
}

export async function installApp(name: string): Promise<void> {
  await fetchJSON('/api/apps/' + name + '/install', {
    method: 'POST',
    body: '{}',
  });
}

export async function uninstallApp(name: string): Promise<void> {
  await fetchJSON('/api/apps/' + name + '/uninstall', {
    method: 'POST',
    body: '{}',
  });
}

export async function waitForApp(
  name: string,
  status: string,
  timeoutMs = 10 * 60_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let last: InstalledApp | null = null;
  while (Date.now() < deadline) {
    const app = await getApp(name);
    if (app) last = app;
    if (app?.status === status) return;
    if (app?.status === 'error') {
      // Terminal by design: the orchestrator records `error` only on a
      // node failure and never recovers without a new install intent.
      // Keep waiting can never converge: fail now with the recorded
      // cause so the report names the real failure instead of a timeout.
      throw new Error(
        `${name} reached terminal "error" state: ${app.last_error || '(no error recorded)'}`,
      );
    }
    await delay(3_000);
  }
  throw new Error(
    `Timed out waiting for ${name} to reach "${status}" after ${timeoutMs}ms` +
      `(last state: ${last ? `${last.status}${last.last_error ? `: ${last.last_error}` : ''}` : 'not installed'})`,
  );
}

export async function ensureInstalled(name: string): Promise<void> {
  const status = await getAppStatus(name);
  if (status === 'running') return;
  // Missing, errored, or mid-transition: submit an install intent (the
  // orchestrator's reset of ERROR nodes makes install idempotent: it is
  // the "Retry install" recovery path) and wait for convergence.
  await installApp(name);
  await waitForApp(name, 'running');
}
