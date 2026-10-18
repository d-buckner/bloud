// SPDX-License-Identifier: AGPL-3.0-only
//
// API calls go directly to the host-agent. Being loopback/trusted is a *scope*,
// not a credential: admin calls must present the runtime's API token, which the
// secrets manager writes next to secrets.json (host-agent-api-token).
import { execFile } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { promisify } from 'node:util';
import { BLOUD_API_URL } from './config';

const run = promisify(execFile);

function delay(ms: number): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  setTimeout(resolve, ms);
  return promise;
}

const BASE_URL = BLOUD_API_URL;

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

/**
 * The live operation the orchestrator is running for an app, when it runs one.
 *
 * A caller reads this to answer "why is this app not running" without reading
 * the host-agent log: `cause` carries the recorded reason, and `phase` says how
 * far the attempt got.
 */
export interface AppOperation {
  node: string;
  type: string;
  phase: string;
  status: string;
  retryable?: boolean;
  cause?: string;
}

export interface InstalledApp {
  catalog_id: string;
  display_name: string;
  status: string;
  /** Set by the orchestrator when the app hits a terminal error. */
  last_error?: string;
  /** The operation in flight or the last one that ran, when there is one. */
  operation?: AppOperation | null;
  port?: number;
  is_system: boolean;
}

/** One catalog entry, as `GET /api/apps` reports it (camelCase, unlike the
 * installed list). A check reads the SSO strategy from here to decide which
 * sign-in a page should show. */
export interface CatalogApp {
  catalogId: string;
  displayName: string;
  description?: string;
  category?: string;
  port?: number;
  isSystem?: boolean;
  sso?: { strategy?: string };
}

async function getApp(name: string): Promise<InstalledApp | null> {
  const body = await fetchJSON<{ apps: InstalledApp[] }>('/api/apps/installed');
  return body.apps.find((a) => a.catalog_id === name) ?? null;
}

/** Every installed app, system apps included. */
export async function listInstalledApps(): Promise<InstalledApp[]> {
  const body = await fetchJSON<{ apps: InstalledApp[] }>('/api/apps/installed');
  return body.apps;
}

/** Every app the catalog offers. */
export async function listCatalogApps(): Promise<CatalogApp[]> {
  const body = await fetchJSON<{ apps: CatalogApp[] }>('/api/apps');
  return body.apps;
}

/**
 * The instance health: `ok`, or `unhealthy` when the database or the intent loop
 * is down. A 200 means the bootstrap gate is open, not that the system is well;
 * this reads the same check the endpoint runs.
 */
export async function getHealth(): Promise<{ status: string }> {
  return fetchJSON<{ status: string }>('/api/health');
}

/**
 * Whether the instance is provisioned and whether the identity provider answers.
 *
 * `authentikReady` is a live probe of the provider, so this is the read that
 * separates "the API is up" from "a user could actually sign in".
 */
export async function getSetupStatus(): Promise<{
  setupRequired: boolean;
  authentikReady: boolean;
  authReady: boolean;
}> {
  return fetchJSON<{
    setupRequired: boolean;
    authentikReady: boolean;
    authReady: boolean;
  }>('/api/setup/status');
}

/**
 * The host-versus-container DNS report the API offers as a system warning.
 *
 * A podman sandbox that captured stale resolver upstreams at boot shows up here
 * before it shows up as an app that cannot reach its OIDC issuer.
 */
export async function getSystemDiagnostics(): Promise<Record<string, unknown>> {
  return fetchJSON<Record<string, unknown>>('/api/system/diagnostics');
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

/** One raw call to the host-agent API, with the status and the body as text. */
export interface ApiResponse {
  method: string;
  path: string;
  status: number;
  ok: boolean;
  body: string;
}

/**
 * Call any host-agent endpoint and hand back what came back.
 *
 * The typed helpers above are what a test asserts on. This is the escape hatch
 * for a one-off look at an endpoint the harness has not wrapped yet, and for an
 * endpoint whose answer is not JSON.
 */
export async function request(method = 'GET', path: string, body?: string): Promise<ApiResponse> {
  const sendsBody = body !== undefined && !['GET', 'HEAD'].includes(method.toUpperCase());
  const resp = await fetch(`${BASE_URL}${path}`, {
    method,
    body: sendsBody ? body : undefined,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${await apiToken()}`,
    },
  });
  return { method, path, status: resp.status, ok: resp.ok, body: await resp.text() };
}

/** The address setting as the API reports it. */
export interface PublicURLSettings {
  url: string;
}

/**
 * The configured address, read straight from the API. Callers use this instead
 * of the settings widget so an assertion checks what is stored rather than what
 * the UI is echoing back.
 */
export async function getPublicURL(): Promise<PublicURLSettings> {
  return fetchJSON<PublicURLSettings>('/api/settings/public-url');
}

/**
 * Save the address and return the value the API reports after the
 * orchestrator has applied it. The save is an intent, so this waits for the
 * live value to reach the canonical one the PUT returned.
 */
export async function savePublicURLAndWait(url: string): Promise<PublicURLSettings> {
  const res = await fetchJSON<{ intentId: string; url: string }>(
    '/api/settings/public-url',
    { method: 'PUT', body: JSON.stringify({ url }) },
  );
  const deadline = Date.now() + 30_000;
  for (;;) {
    const live = await getPublicURL();
    if (live.url === res.url) return live;
    if (Date.now() >= deadline) {
      throw new Error(`address never converged: wanted ${res.url}, still ${live.url}`);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
}

/** The time zone setting as the API reports it. An empty zone is a value: it
 * means Bloud states no zone and every container keeps its image default. */
export interface TimezoneSettings {
  zone: string;
}

/**
 * The configured time zone, read straight from the API rather than from the
 * settings widget, so a check tests what is stored.
 */
export async function getTimezone(): Promise<TimezoneSettings> {
  return fetchJSON<TimezoneSettings>('/api/settings/timezone');
}

/**
 * Save the time zone and return the value the API reports after the orchestrator
 * applied it. The save is an intent, and a zone that changed recreates every
 * managed container, so the window is wider than the address setting's.
 */
export async function saveTimezoneAndWait(zone: string): Promise<TimezoneSettings> {
  const res = await fetchJSON<{ intentId: string; zone: string }>('/api/settings/timezone', {
    method: 'PUT',
    body: JSON.stringify({ zone }),
  });
  const deadline = Date.now() + 60_000;
  for (;;) {
    const live = await getTimezone();
    if (live.zone === res.zone) return live;
    if (Date.now() >= deadline) {
      throw new Error(`time zone never converged: wanted ${res.zone}, still ${live.zone}`);
    }
    await new Promise((r) => setTimeout(r, 500));
  }
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

/**
 * One client credential, as the reveal endpoint hands it over. This is the only
 * API call here that returns a credential value: the descriptor list at the
 * same path deliberately has no value field at any reveal policy.
 *
 * Under `reveal: once` (the policy every app in the catalog declares) this is
 * single-use per value: a second reveal of the same credential answers 409, and
 * only a rotation produces one that can be shown again. A caller that needs the
 * value therefore has to ask before anything else has.
 */
export interface ClientCredential {
  secret: string;
  value: string;
  snippet: string;
}

export async function revealClientCredential(app: string, secret: string): Promise<ClientCredential> {
  return fetchJSON<ClientCredential>(
    `/api/apps/${app}/client-credentials/${secret}/reveal`,
    { method: 'POST', body: '{}' },
  );
}

/**
 * Rotates a client credential and returns the new value, which a rotation always
 * hands over (that is what makes a lost credential recoverable).
 *
 * The call is not free: rotation ends every live session the app holds and forces
 * the recreate that installs whatever change the rotation implies. A caller should
 * reach for it only when the reveal is already spent.
 */
export async function rotateClientCredential(app: string, secret: string): Promise<ClientCredential> {
  return fetchJSON<ClientCredential>(
    `/api/apps/${app}/client-credentials/${secret}/rotate`,
    { method: 'POST', body: '{}' },
  );
}

// External apps (launchers only). A launcher is a tile that opens a URL.
export interface Launcher {
  id: string;
  name: string;
  url: string;
  icon: string;
}

export async function listLaunchers(): Promise<Launcher[]> {
  return fetchJSON<Launcher[]>('/api/external-apps');
}

export async function addLauncher(input: { name: string; url: string; icon?: string }): Promise<void> {
  await fetchJSON('/api/external-apps', {
    method: 'POST',
    body: JSON.stringify({ name: input.name, url: input.url, icon: input.icon ?? '' }),
  });
}

export async function removeLauncher(id: string): Promise<void> {
  await fetchJSON('/api/external-apps/' + id, { method: 'DELETE' });
}

// Transcription: the one endpoint the instance declares for speech-to-text.
export interface TranscriptionSettings {
  configured: boolean;
  name: string;
  baseUrl: string;
  model: string;
  hasApiKey: boolean;
  servedTo: string[];
}

export async function getTranscriptionSettings(): Promise<TranscriptionSettings> {
  return fetchJSON<TranscriptionSettings>('/api/settings/transcription');
}

/**
 * One read of the whole instance: health, provisioning, the settings, and every
 * app. A field that could not be read carries `{ error }` instead of throwing,
 * because the reason one read failed is the thing a reader needs most.
 */
export interface StatusSnapshot {
  health: { status: string } | { error: string };
  setup: { setupRequired: boolean; authentikReady: boolean; authReady: boolean } | { error: string };
  publicUrl: string | { error: string };
  timezone: string | { error: string };
  transcription: TranscriptionSettings | { error: string };
  diagnostics: Record<string, unknown> | { error: string };
  apps: InstalledApp[] | { error: string };
}

async function attempt<T>(read: () => Promise<T>): Promise<T | { error: string }> {
  try {
    return await read();
  } catch (err) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
}

export async function collectStatus(): Promise<StatusSnapshot> {
  const [health, setup, url, zone, transcription, diagnostics, apps] = await Promise.all([
    attempt(getHealth),
    attempt(getSetupStatus),
    attempt(async () => (await getPublicURL()).url),
    attempt(async () => (await getTimezone()).zone),
    attempt(getTranscriptionSettings),
    attempt(getSystemDiagnostics),
    attempt(listInstalledApps),
  ]);
  return { health, setup, publicUrl: url, timezone: zone, transcription, diagnostics, apps };
}

/**
 * Saves the transcription endpoint and returns the state the orchestrator applied.
 *
 * The save is an intent: the PUT answers 202 and the write lands on the
 * orchestrator's queue, so a read-back fired straight after it reports whatever
 * the queue has reached, which is usually the previous value. That is the failure
 * where a check passes for the wrong reason or fails for one it cannot name: the
 * assertion reads an empty endpoint seconds before the save is stored. So this
 * polls until the stored state matches what the save reported, the same way
 * `savePublicURLAndWait` does, and a payload that was dropped or rewritten fails
 * here with both values in the message instead of as a bare mismatch later.
 *
 * The credential is waited on only when the request carried one. An omitted
 * `apiKey` means "keep the stored key", so its presence is not this save's to
 * wait for.
 */
export async function setTranscriptionSettings(body: {
  baseUrl: string;
  name?: string;
  model?: string;
  apiKey?: string;
}): Promise<TranscriptionSettings> {
  const accepted = await fetchJSON<{
    baseUrl: string;
    model: string;
    configured: boolean;
  }>('/api/settings/transcription', { method: 'PUT', body: JSON.stringify(body) });

  const deadline = Date.now() + 30_000;
  for (;;) {
    const live = await getTranscriptionSettings();
    const applied =
      live.baseUrl === accepted.baseUrl &&
      live.model === accepted.model &&
      live.configured === accepted.configured &&
      (body.apiKey === undefined || live.hasApiKey === (body.apiKey !== ''));
    if (applied) return live;
    if (Date.now() >= deadline) {
      throw new Error(
        `transcription settings never converged: wanted ${accepted.baseUrl || 'unset'}, ` +
          `still ${live.baseUrl || 'unset'}`,
      );
    }
    await delay(500);
  }
}
