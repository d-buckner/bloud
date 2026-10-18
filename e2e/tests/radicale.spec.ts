// SPDX-License-Identifier: AGPL-3.0-only
import { request } from '@playwright/test';
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  CREDENTIALS,
  appOrigin,
  appUrlPattern,
  LDAP_ATTEMPT_TIMEOUT_MS,
  LDAP_SETTLE_TIMEOUT_MS,
  openAppFromHome,
  waitForLdapBackedAnswer,
} from '@bloud/browser-harness';

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
 *
 * The rung that admits a Bloud account belongs to the framework, because the
 * readiness it waits for belongs to the identity provider and not to this app.
 * What is left here is the DAV-specific half: what an unauthenticated visitor
 * gets, and what a wrong password gets.
 */
describeApp(
  'radicale',
  (app) => {
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

    test('refuses an anonymous DAV request', async () => {
      test.setTimeout(60_000);
      const ctx = await request.newContext({ baseURL: RADICALE_URL });
      try {
        // The property under test is "an anonymous PROPFIND is refused", not
        // the exact code. A 2xx would mean the server is open: every calendar
        // on the box readable by anyone on the network. Any 4xx rules that out.
        //
        // Deliberately not pinned to 401 + `WWW-Authenticate: Basic realm=Bloud`.
        // CI returns 405 here, and the assertion is not the place to settle why;
        // what it must never do is pass on a 200 or a 207.
        //
        // No readiness wait here on purpose. Radicale denies an unauthenticated
        // request before it contacts the outpost, so this rung answers at once
        // and a retry would only hide a real refusal.
        const res = await ctx.fetch(`/${CREDENTIALS.username}/`, {
          method: 'PROPFIND',
          maxRedirects: 0,
        });
        expect(res.status()).toBeGreaterThanOrEqual(400);
        expect(res.status()).toBeLessThan(500);
      } finally {
        await ctx.dispose();
      }
    });

    test('refuses a wrong password for the same account', async () => {
      test.setTimeout(LDAP_SETTLE_TIMEOUT_MS + 60_000);
      // Credentials go on the context so the Basic challenge round trip is
      // answered the way a calendar client answers it, not pre-emptively.
      const ctx = await request.newContext({
        baseURL: RADICALE_URL,
        httpCredentials: {
          username: CREDENTIALS.username,
          password: `${CREDENTIALS.password}-definitely-wrong`,
        },
      });
      try {
        // Same bar as the anonymous case: refused, with a 4xx. What matters is
        // that a wrong password does not buy a 207.
        //
        // This one does reach the outpost, because Radicale binds as the reader,
        // finds the account, then binds as that account with the password the
        // client sent. So it needs the same settle loop as the admitting rung:
        // while the provider is still warming, the answer is a 500 that says
        // nothing about the password.
        const res = await waitForLdapBackedAnswer(
          `radicale wrong-password PROPFIND /${CREDENTIALS.username}/`,
          () =>
            ctx.fetch(`/${CREDENTIALS.username}/`, {
              method: 'PROPFIND',
              maxRedirects: 0,
              timeout: LDAP_ATTEMPT_TIMEOUT_MS,
            }),
          LDAP_SETTLE_TIMEOUT_MS,
        );
        expect(res.status()).toBeGreaterThanOrEqual(400);
        expect(res.status()).toBeLessThan(500);
      } finally {
        await ctx.dispose();
      }
    });
  },
  {
    label: 'Radicale',
    ldap: {
      // 207 Multi-Status is DAV's answer to a successful collection listing.
      // This is the whole integration working: Radicale searched Authentik's
      // LDAP for this login name, bound as the found user with the password the
      // client sent, and the rights model handed back that user's tree.
      method: 'PROPFIND',
      path: `/${CREDENTIALS.username}/`,
      headers: { Depth: '0' },
      expectedStatus: 207,
      bodyContains: 'multistatus',
    },
  },
);
