// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled, getAppStatus } from '../lib/api';
import { appHost } from '../lib/origin';
import {
  expectForwardAuthPrompt,
  signInThroughForwardAuth,
} from '../lib/forwardAuth';

// Calino is a browser CalDAV client: a static bundle with no login of its own
// and no storage on the server. Everything Bloud does for it happens at the
// edges of that bundle, and both edges are observable:
//
//   1. the required `caldav` integration, which makes the graph install the
//      DAV server together with the client;
//   2. forward-auth at the ingress, because the app keeps the user's CalDAV
//      password in browser storage and must not be loadable by anyone who
//      cannot sign in.
//
// What the app does after that, talking DAV to Radicale with the user's own
// credential, is the provider's contract and is covered in apps/radicale.
describeApp('calino', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the rungs below are skipped, which distinguishes a
    // broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('calino');
  });

  test('installing the client brought its CalDAV server with it', async () => {
    test.setTimeout(12 * 60_000);
    // The requirement is declared in apps/calino/metadata.yaml as
    // `integrations.caldav.required` with radicale as the default provider,
    // and the orchestrator records that provider before the app itself. Assert
    // the server exists rather than that a field in a file says so: a calendar
    // client installed with no DAV server behind it opens onto an empty window,
    // and that is the exact outcome the requirement exists to prevent.
    const status = await getAppStatus('radicale');
    expect(status, 'radicale is a required provider of calino').not.toBeNull();
    await ensureInstalled('radicale');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Calino');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Calino');
  });

  test('forward-auth gates the app: the popup lands on the login prompt', async () => {
    test.setTimeout(120_000);
    const calino = await openAppFromHome(app.page, 'Calino');
    try {
      // A fresh popup has no session, so the ingress must intercept it and
      // send it to the identity provider rather than serving the bundle.
      await expectForwardAuthPrompt(calino);
    } finally {
      await calino.close();
    }
  });

  test('signs in through forward-auth and reaches the Calino bundle', async () => {
    test.setTimeout(300_000);
    const calino = await openAppFromHome(app.page, 'Calino');

    // After the prompt, the Calino SPA itself is served and mounted. The
    // mount check is the one that matters: the shell ships with an empty
    // `#root`, so `:not(:empty)` is only true once the bundle actually ran.
    // A Caddy error page or a wrong document root passes a title check far
    // more easily than it passes this.
    await signInThroughForwardAuth(calino, {
      origin: appHost('calino'),
      title: /Calino/i,
      appShell: '#root:not(:empty)',
    });
  });
});
