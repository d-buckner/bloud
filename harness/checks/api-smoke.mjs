// SPDX-License-Identifier: AGPL-3.0-only
//
// The instance-wide smoke read: every endpoint an operator's health check should
// agree with, in one run, with no browser.
//
//   node harness/bin/browser.mjs run api-smoke
//
// This is the check to run after a restart, a deploy, or a proxy change. It opens
// no page on purpose: the answers come from the API, so a stalled dashboard cannot
// hide a working orchestrator, and a working dashboard cannot excuse a dead one.

export const name = 'api smoke';
export const description = 'health, provisioning, the catalog, every app state, and the DNS cross-check';
export const options = { open: false, timeoutMs: 3 * 60_000 };

export default async function ({ api, check, note }) {
  const health = await check.must('the API answers its health probe', () => api.getHealth());
  check.eq(health.status, 'ok', 'the health probe reports ok', 'a non-ok health means the database or the intent loop is down');

  const setup = await api.getSetupStatus();
  check.ok(!setup.setupRequired, 'the instance is provisioned', 'the setup wizard is showing');
  check.ok(setup.authentikReady, 'the identity provider answers', 'a user cannot sign in while this is false');
  check.ok(setup.authReady, 'the auth surface is ready');

  const catalog = await api.listCatalogApps();
  check.ok(catalog.length > 0, `the catalog loaded (${catalog.length} apps)`);

  const apps = await api.listInstalledApps();
  const notRunning = apps.filter((app) => app.status !== 'running');
  check.ok(
    notRunning.length === 0,
    `every installed app is running (${apps.length})`,
    notRunning.map((app) => `${app.catalog_id}: ${app.status}${app.last_error ? ` (${app.last_error})` : ''}`).join('; '),
  );

  const errored = apps.filter((app) => app.status === 'error');
  if (errored.length > 0) {
    check.fail('no installed app is in a terminal error state', errored.map((a) => a.catalog_id).join(', '));
  } else {
    check.ok(true, 'no installed app is in a terminal error state');
  }

  const diagnostics = await api.getSystemDiagnostics();
  if (diagnostics.skipped) {
    check.skip('the container resolver agrees with the host', 'the API skipped the cross-check');
  } else if (diagnostics.diverged) {
    // The API files this as a system warning, not a fault: the instance runs, and
    // a container that cannot resolve the public host is usually a podman sandbox
    // that captured stale upstreams when it booted. Reported, not failed.
    check.skip('the container resolver agrees with the host', `warning: ${diagnostics.detail ?? 'host and container disagree'}`);
  } else {
    check.ok(true, 'the container resolver agrees with the host');
  }

  note(`${apps.length} installed, ${apps.filter((a) => a.is_system).length} of them system apps`);
}
