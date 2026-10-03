// SPDX-License-Identifier: AGPL-3.0-only
import { expect, test } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled, getAppStatus } from '../lib/api';
import { appOrigin } from '../lib/origin';

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
describeApp(
  'calino',
  (app) => {
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

    test('serves the CORS preflight the browser client needs', async () => {
      test.setTimeout(60_000);
      // Calino is a static SPA, so its DAV calls cross origins and the browser
      // refuses the response unless Radicale sends CORS headers. Assert the
      // preflight through the real proxy: the header only helps if it survives
      // Traefik. The preflight carries no credentials, so it must answer 200
      // before the client has presented a password.
      const res = await app.page.request.fetch(`${appOrigin('radicale')}/`, {
        method: 'OPTIONS',
        headers: {
          Origin: appOrigin('calino'),
          'Access-Control-Request-Method': 'PROPFIND',
          'Access-Control-Request-Headers': 'authorization,depth',
        },
        failOnStatusCode: false,
      });
      expect(res.status(), 'the anonymous OPTIONS preflight must not require auth').toBe(200);
      expect(res.headers()['access-control-allow-origin']).toBe('*');
      expect(res.headers()['access-control-allow-headers']).toContain('authorization');
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
  },
  {
    // `#root:not(:empty)`: the shell ships with an empty mount div, so this
    // is only true once the bundle actually ran. A Caddy error page or a
    // wrong document root passes a title check far more easily than this.
    forwardAuth: {
      label: 'Calino',
      title: /Calino/i,
      appShell: '#root:not(:empty)',
    },
  },
);
