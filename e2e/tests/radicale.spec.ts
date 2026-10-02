// SPDX-License-Identifier: AGPL-3.0-only
import { request } from '@playwright/test';
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { appOrigin, appUrlPattern } from '../lib/origin';
import { TEST_CREDS } from './constants';

const RADICALE_URL = appOrigin('radicale');

/**
 * Radicale is not a dashboard app: its product surface is CalDAV and CardDAV,
 * which a person configures in a calendar client, not in a browser. What this
 * suite can honestly assert through a browser is the two things that make the
 * app usable or unsafe: that it answers at all, and that it answers the way an
 * LDAP-backed DAV server must.
 *
 * The DAV assertions go over real HTTP through Traefik with the same
 * credentials a calendar client would send. That is the round trip the app
 * exists for; a screenshot of its web UI would prove far less.
 */
describeApp('radicale', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the rungs below are skipped, which distinguishes a
    // broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('radicale');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Radicale');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    // Navigate explicitly: the catalog test above leaves the shared page on
    // /catalog, and `.app-slot` is a home-screen element that does not exist
    // there.
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Radicale');
  });

  test('opens from the home tile and serves its web UI', async () => {
    test.setTimeout(180_000);
    const radicale = await openAppFromHome(app.page, 'Radicale');
    try {
      // The popup is really Radicale and the proxy path is live. The app
      // redirects its root to the built-in web UI rather than serving a
      // directory listing or an error page.
      await expect(radicale).toHaveURL(appUrlPattern('radicale'), {
        timeout: 30_000,
      });
      await radicale.waitForLoadState('domcontentloaded');
      expect(radicale.url()).toContain('radicale');
    } finally {
      await radicale.close();
    }
  });

  test('challenges an anonymous DAV request with the Bloud realm', async () => {
    test.setTimeout(60_000);
    const ctx = await request.newContext({ baseURL: RADICALE_URL });
    try {
      // The response is the proof that the [auth] section the configurator
      // wrote is what the running process loaded. An anonymous PROPFIND that
      // returned 207 would mean the server is open: every calendar on the
      // box readable by anyone on the network.
      const res = await ctx.fetch(`/${TEST_CREDS.USERNAME}/`, {
        method: 'PROPFIND',
        maxRedirects: 0,
      });
      expect(res.status()).toBe(401);
      expect(res.headers()['www-authenticate']).toContain('Basic');
      expect(res.headers()['www-authenticate']).toContain('Bloud');
    } finally {
      await ctx.dispose();
    }
  });

  test('admits a Bloud account over DAV with its own calendar root', async () => {
    test.setTimeout(120_000);
    // Credentials go on the context so the Basic challenge round trip is
    // answered the way a calendar client answers it, not pre-emptively.
    const ctx = await request.newContext({
      baseURL: RADICALE_URL,
      httpCredentials: {
        username: TEST_CREDS.USERNAME,
        password: TEST_CREDS.PASSWORD,
      },
    });
    try {
      // 207 Multi-Status is DAV's answer to a successful collection listing.
      // This is the whole integration working: Radicale searched Authentik's
      // LDAP for this login name, bound as the found user with the password
      // the client sent, and the rights model handed back that user's tree.
      const res = await ctx.fetch(`/${TEST_CREDS.USERNAME}/`, {
        method: 'PROPFIND',
        headers: { Depth: '0' },
      });
      expect(res.status()).toBe(207);
      expect(await res.text()).toContain('multistatus');
    } finally {
      await ctx.dispose();
    }
  });

  test('refuses a wrong password for the same account', async () => {
    test.setTimeout(60_000);
    const ctx = await request.newContext({
      baseURL: RADICALE_URL,
      httpCredentials: {
        username: TEST_CREDS.USERNAME,
        password: `${TEST_CREDS.PASSWORD}-definitely-wrong`,
      },
    });
    try {
      // The failure has to be an auth failure, not a 404 or a redirect: the
      // user exists in the directory and the password is what did not match.
      const res = await ctx.fetch(`/${TEST_CREDS.USERNAME}/`, {
        method: 'PROPFIND',
        maxRedirects: 0,
      });
      expect(res.status()).toBe(401);
    } finally {
      await ctx.dispose();
    }
  });
});
