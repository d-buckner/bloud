// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled } from '../lib/api';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Navidrome uses
// forward-auth: every request to the app origin is checked against
// Authentik before it reaches the container, so the popup opens on the
// Authentik prompt: the prompt appearing is itself the observable
// behavior of the auth rung, and completing it is the sign-in rung. Both
// are the framework's forward-auth rungs.
describeApp(
  'navidrome',
  (app) => {
    test('converges to running', async () => {
      // Infrastructure rung: fresh-VM image pull + first-run convergence.
      // When this fails, the UI rungs below are skipped, which distinguishes
      // a broken install from a misbehaving app.
      test.setTimeout(12 * 60_000);
      await ensureInstalled('navidrome');
    });

    test('appears in the catalog as installed', async () => {
      test.setTimeout(60_000);
      await expectInstalledInCatalog(app.page, 'Navidrome');
    });

    test('appears on the home screen as a converged tile', async () => {
      test.setTimeout(60_000);
      await app.page.goto('/');
      await expectRunningTile(app.page, 'Navidrome');
    });
  },
  {
    forwardAuth: {
      label: 'Navidrome',
      title: /Navidrome/i,
      appShell: '#root, .MuiBox-root, nav',
    },
  },
);
