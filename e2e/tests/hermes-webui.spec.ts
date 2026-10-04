// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled } from '../lib/api';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Hermes Web UI is gated by forward-auth at the ingress. The app's own
// login is a single shared password and its native OIDC client refuses an
// issuer that resolves to a private address, so Bloud supplies the door
// and the app's own password is never set (see apps/hermes-webui/
// INTEGRATION.md). Both forward-auth rungs are the framework's: the popup
// lands on the Authentik prompt rather than on the app, and past that
// prompt the app serves its own UI with no password field of its own.
describeApp(
  'hermes-webui',
  (app) => {
    test('converges to running', async () => {
      // Infrastructure rung: fresh-VM image pull, the pinned Hermes agent
      // source install, and the venv the container builds from PyPI on
      // first boot. When this fails, the UI rungs below are skipped, which
      // distinguishes a broken install from a misbehaving app.
      test.setTimeout(15 * 60_000);
      await ensureInstalled('hermes-webui');
    });

    test('brought Hermes in as the agent it fronts', async () => {
      // The required `agentHome` contract means installing the front end
      // resolves Hermes as its provider and records it first, so a
      // hermes-webui install is also what puts the agent on the instance.
      // Asserted as an observable outcome rather than a metadata read,
      // because the planner running is exactly the thing that could quietly
      // stop happening.
      test.setTimeout(15 * 60_000);
      await expectInstalledInCatalog(app.page, 'Hermes');
    });

    test('appears in the catalog as installed', async () => {
      test.setTimeout(60_000);
      await expectInstalledInCatalog(app.page, 'Hermes Web UI');
    });

    test('appears on the home screen as a converged tile', async () => {
      test.setTimeout(60_000);
      await app.page.goto('/');
      await expectRunningTile(app.page, 'Hermes Web UI');
    });
  },
  {
    // `#composerWrap`: the chat composer, part of the server-rendered
    // shell. It is present as soon as the app serves itself and never
    // appears in an Authentik page, so it separates "the app mounted"
    // from "a page answered".
    forwardAuth: {
      label: 'Hermes Web UI',
      title: /Hermes/i,
      appShell: '#composerWrap',
    },
  },
);
