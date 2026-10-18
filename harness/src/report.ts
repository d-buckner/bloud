// SPDX-License-Identifier: AGPL-3.0-only
//
// The text a person or an agent reads when they ask what an instance looks like.
//
// These are pure functions over API payloads, so a test can pin the wording and
// a recipe stays three lines: read, format, print. The wording matters: a table
// an agent has to re-read twice is a table that wasted the run.
import type { ApiResponse, InstalledApp, StatusSnapshot } from './api';
import { BLOUD_API_URL, BLOUD_URL } from './config';

/** Align one `label  value` line. */
function row(label: string, value: string): string {
  return `  ${label.padEnd(15)}${value}`;
}

/** The reason an app is not simply running, when there is one to report.
 * A stale failed operation behind a running app is not a note worth reading: the
 * state column already says the app came back. */
function appNote(app: InstalledApp): string {
  if (app.last_error) return app.last_error;
  const op = app.operation;
  if (!op || op.status === 'success' || app.status === 'running') return '';
  const cause = op.cause ? `: ${op.cause}` : '';
  return `${op.type}/${op.phase} ${op.status}${cause}`;
}

/** One line per installed app: what it is, what state it is in, and why not. */
export function formatAppTable(apps: InstalledApp[] | { error: string }): string {
  if (!Array.isArray(apps)) return row('apps', `unreadable: ${apps.error}`);
  if (apps.length === 0) return row('apps', 'none installed');
  const lines = [`  ${'APP'.padEnd(18)}${'STATE'.padEnd(12)}${'PORT'.padEnd(8)}NOTE`];
  for (const app of apps) {
    const port = app.port ? `:${app.port}` : '';
    lines.push(
      `  ${app.catalog_id.padEnd(18)}${app.status.padEnd(12)}${port.padEnd(8)}${appNote(app)}`.trimEnd(),
    );
  }
  return lines.join('\n');
}

function describeSetup(setup: StatusSnapshot['setup']): string {
  if ('error' in setup) return `unreadable: ${setup.error}`;
  const parts = [setup.setupRequired ? 'setup wizard is showing' : 'provisioned'];
  parts.push(setup.authentikReady ? 'identity provider ready' : 'identity provider NOT ready');
  parts.push(setup.authReady ? 'auth ready' : 'auth NOT ready');
  return parts.join(', ');
}

function describeTranscription(settings: StatusSnapshot['transcription']): string {
  if ('error' in settings) return `unreadable: ${settings.error}`;
  if (!settings.configured) return 'not configured';
  const served = settings.servedTo?.length ? `, served to ${settings.servedTo.join(', ')}` : '';
  return `${settings.baseUrl || '(no address)'}${settings.model ? ` model ${settings.model}` : ''}${served}`;
}

/**
 * The DNS check as one line.
 *
 * A public host that resolves on the host but not inside a container is a
 * podman sandbox that captured stale resolver upstreams at boot. It shows up
 * as an app that cannot reach its OIDC issuer, which is a much worse way to
 * discover it.
 */
function describeDiagnostics(diagnostics: StatusSnapshot['diagnostics']): string {
  if ('error' in diagnostics) return `unreadable: ${diagnostics.error}`;
  if (diagnostics.skipped) return 'not checked';
  if (diagnostics.diverged) return `DIVERGED: ${diagnostics.detail ?? 'host and container disagree'}`;
  const container = diagnostics.containerChecked ? `container ${diagnostics.container} resolves` : 'container not checked';
  return `host ${diagnostics.hostResolves ? 'resolves' : 'does NOT resolve'}, ${container}`;
}

function value(field: string | { error: string }): string {
  return typeof field === 'string' ? field : `unreadable: ${field.error}`;
}

/** The whole-instance read, as a block a reader can scan in one pass. */
export function formatStatus(snapshot: StatusSnapshot): string {
  const health = 'error' in snapshot.health ? `unreadable: ${snapshot.health.error}` : snapshot.health.status;
  const lines = [
    `INSTANCE  ${BLOUD_URL}  (API ${BLOUD_API_URL})`,
    row('health', health),
    row('setup', describeSetup(snapshot.setup)),
    row('public URL', value(snapshot.publicUrl)),
    row('time zone', value(snapshot.timezone) || '(unset: containers keep their own default)'),
    row('transcription', describeTranscription(snapshot.transcription)),
    row('DNS', describeDiagnostics(snapshot.diagnostics)),
  ];
  if (Array.isArray(snapshot.apps)) {
    const running = snapshot.apps.filter((a) => a.status === 'running').length;
    lines.push(row('apps', `${snapshot.apps.length} installed, ${running} running`));
  }
  lines.push(formatAppTable(snapshot.apps));
  return lines.join('\n');
}

/**
 * One raw API response, printed so it can be read and piped.
 *
 * JSON is re-indented; anything else is passed through. A non-2xx is written on
 * stderr, so `browser.mjs api ... > out.json` leaves a clean file and a loud
 * failure.
 */
export function formatApiResponse(response: ApiResponse): string {
  let body = response.body;
  try {
    body = JSON.stringify(JSON.parse(response.body), null, 2);
  } catch {
    // Not JSON: the body is already the most readable form of itself.
  }
  const status = `${response.method} ${response.path} -> ${response.status}`;
  return body ? `${status}\n${body}` : status;
}
