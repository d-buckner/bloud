// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import {
  expectForwardAuthPrompt,
  signInThroughForwardAuth,
} from '../lib/forwardAuth';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Sonarr is gated by forward-auth at the ingress, and Bloud's
// configurator provisions its config.xml with AuthenticationMethod
// External, so the container treats an already-authenticated request as
// authenticated and never shows its own login form. Both halves are
// observable and both are asserted: the popup lands on the Authentik
// prompt (not on Sonarr), and after that prompt is completed Sonarr
// serves its own UI with no visible password field of its own.
describeApp('sonarr', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs below are skipped, which distinguishes
    // a broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('sonarr');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Sonarr');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Sonarr');
  });

  test('forward-auth gates the app: the popup lands on the login prompt', async () => {
    test.setTimeout(120_000);
    const sonarr = await openAppFromHome(app.page, 'Sonarr');
    try {
      // A fresh popup has no Sonarr session, so forward-auth must
      // intercept it: the popup round-trips to the Authentik flow on the
      // Sonarr origin instead of serving the app.
      await expectForwardAuthPrompt(sonarr);
    } finally {
      await sonarr.close();
    }
  });

  test('signs in through forward-auth and reaches the Sonarr UI', async () => {
    test.setTimeout(300_000);
    const sonarr = await openAppFromHome(app.page, 'Sonarr');

    // Completing the prompt must serve Sonarr itself: the popup URL is
    // the Sonarr origin, the document title is Sonarr's, Sonarr's React
    // shell actually rendered (`#root:not(:empty)`: the mount div ships
    // empty and is only filled once the app answers `initialize.json`,
    // and Forms auth would instead serve the separate login page with no
    // `#root` at all), and no visible password field exists, proving
    // Sonarr did not demand its own credentials.
    await signInThroughForwardAuth(sonarr, {
      origin: 'sonarr.localhost:8080',
      title: /Sonarr/i,
      appShell: '#root:not(:empty)',
    });
  });
});
