// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { BrowserContext, Page } from '@playwright/test';
import { createSign, generateKeyPairSync } from 'node:crypto';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';
import { appOrigin, appUrlPattern } from '../lib/origin';

const HERMES_ORIGIN = appOrigin('hermes');

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Hermes is a native-oidc app that joins Authentik as a PUBLIC PKCE client
// through the Hermes dashboard's bundled self-hosted OIDC provider. The
// dashboard enforces its own auth gate on the bind Bloud reaches it through,
// and that gate hands the browser to the shared Authentik issuer, so the
// observable Bloud contract is the same shape as the other native-oidc apps:
// the gate is on, and a user can complete the Authentik round-trip and land
// back authenticated. The issuer origin is the host loopback
// (localhost:8080), which the dashboard reaches because the container runs
// with the host network namespace (catalog sso.loopbackIssuer).
//
// The sign-in rungs use an isolated browser context. The shared suite context
// authenticated as the E2E user carries an Authentik session, and the
// dashboard's gate hands such a visitor straight through; only a session-less
// context observes the sign-in prompt and exercises the credential exchange.
describeApp('hermes', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence
    // through the dependency-graph install path, including the public
    // native-oidc client registration with Authentik and the config.yaml
    // the configurator writes. A green converge means the SSO-backed
    // dashboard booted with its provider configured (the gate would fail
    // closed otherwise) and the orchestrator promoted the node to RUNNING.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('hermes');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Hermes');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Hermes');
  });

  test('the auth gate is on: a session-less visitor is handed to sign-in', async () => {
    test.setTimeout(120_000);
    const ctx = await newIsolatedContext(app);
    const hermes = await ctx.newPage();
    try {
      // No Bloud and no Authentik session here, so the dashboard's gate must
      // intercept: its single registered provider hands the visitor to the
      // shared Authentik flow on the loopback issuer origin instead of
      // serving the app.
      await hermes.goto(`${HERMES_ORIGIN}/`);
      await new LoginPage(hermes).usernameField.waitFor({
        state: 'visible',
        timeout: 60_000,
      });
    } finally {
      await ctx.close();
    }
  });

  test('signs in through Authentik and lands back authenticated', async () => {
    test.setTimeout(360_000);
    const ctx = await newIsolatedContext(app);
    const hermes = await ctx.newPage();
    try {
      // Start at the app origin: the gate bounces an unauthenticated visitor
      // into the self-hosted provider's authorization-code + PKCE flow, which
      // is the shared Authentik issuer, not the Nous Portal.
      await hermes.goto(`${HERMES_ORIGIN}/`);

      // Complete the Authentik login on the issuer origin. The flow hops
      // through steps that re-render after document load, so poll within a
      // deadline rather than checking once.
      const loginPage = new LoginPage(hermes);
      const deadline = Date.now() + 240_000;
      for (;;) {
        const url = hermes.url();
        if (url.startsWith(HERMES_ORIGIN) && !url.includes('/login') && !url.includes('/auth/')) break;
        if (Date.now() > deadline) break;
        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }
        await hermes.waitForTimeout(500);
      }

      // Back on the app origin, off the sign-in route.
      await expect(hermes).toHaveURL(appUrlPattern('hermes'), {
        timeout: 120_000,
      });
      await expect(hermes).not.toHaveURL(/\/login/, { timeout: 120_000 });

      // The dashboard authenticates the session against the issuer, so a
      // successful /api/auth/me is the proof the round-trip established a
      // real session (landing off /login alone would also match a dead page).
      const status = await hermes.evaluate(
        async () => (await fetch('/api/auth/me')).status,
      );
      expect(status).toBe(200);
    } finally {
      await ctx.close();
    }
  });

  test('an app-level 503 reaches the browser, not the waiting page', async () => {
    // The app is up here: the gateway is running, the dashboard process is
    // healthy, and Traefik can reach it. This is not a container coming back.
    //
    // What Bloud must not do is answer for the app. The waiting-page middleware
    // exists for the case where Traefik could not reach the container at all,
    // and it replaces whatever came back with a page that reloads on its own.
    // When the app itself produced the status, that page is a lie: the reload
    // lands on the same answer every time, so the tab sits on "Hermes is
    // re-loading" over an install that has been healthy for hours while the
    // actual fault (the identity provider cannot vouch for this session) is
    // never shown to anyone.
    test.setTimeout(120_000);
    const ctx = await newIsolatedContext(app);
    const hermes = await ctx.newPage();
    try {
      // Prove the app is reachable first, so the rung cannot pass by accident
      // on an app that is genuinely down. /api/health is Hermes' public
      // liveness route, exempt from its auth gate by design.
      const health = await hermes.request.get(`${HERMES_ORIGIN}/api/health`);
      expect(health.status()).toBe(200);

      await ctx.addCookies([
        { name: 'hermes_session_at', value: unverifiableSessionToken(), url: HERMES_ORIGIN },
      ]);

      const doc = await hermes.goto(`${HERMES_ORIGIN}/`, { waitUntil: 'domcontentloaded' });
      expect(doc).not.toBeNull();

      // Bloud did not answer for the app: no waiting-page marker, and none of
      // its prose in the body.
      expect(doc!.headers()).not.toHaveProperty('x-bloud-loading');
      expect(await doc!.text()).not.toContain('is re-loading');

      // And the app's own answer arrived. 503 is Hermes' documented response
      // for "my identity provider is unreachable", and its body names the
      // provider, which is the diagnosis the waiting page was hiding.
      expect(doc!.status()).toBe(503);
      expect(await doc!.text()).toContain('unreachable');
    } finally {
      await ctx.close();
    }
  });
});

/**
 * A Hermes session token that Hermes' identity provider cannot vouch for.
 *
 * Hermes verifies its dashboard session against the issuer's JWKS. When the
 * provider no longer publishes the key a token was signed with, that lookup
 * fails with "Unable to find a signing key that matches", Hermes classifies it
 * as a *provider outage* rather than a bad session, and answers 503 while
 * deliberately keeping the cookie, because a real IdP blip must not log every
 * signed-in user out. The cookie surviving is what makes it permanent: every
 * later request 503s the same way until the session itself changes.
 *
 * An RS256 JWT signed here with a throwaway key, naming a `kid` the issuer has
 * never heard of, reproduces that on any deployment without touching Authentik.
 * The signature lookup happens before any claim is read, so the issuer and
 * audience in the payload do not change the outcome.
 */
function unverifiableSessionToken(): string {
  const { privateKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const b64 = (input: Buffer) => input.toString('base64url');
  const header = b64(
    Buffer.from(
      JSON.stringify({ alg: 'RS256', typ: 'JWT', kid: 'bloud-e2e-key-the-issuer-never-had' }),
    ),
  );
  const now = Math.floor(Date.now() / 1000);
  const payload = b64(
    Buffer.from(
      JSON.stringify({
        iss: 'https://issuer.invalid/application/o/hermes/',
        sub: 'e2e@bloud.invalid',
        aud: 'hermes-client',
        email: 'e2e@bloud.invalid',
        iat: now,
        exp: now + 3600,
      }),
    ),
  );
  const signer = createSign('RSA-SHA256');
  signer.update(`${header}.${payload}`);
  return `${header}.${payload}.${b64(signer.sign(privateKey))}`;
}

// newIsolatedContext opens a browser context with no inherited cookies, so the
// gate and sign-in rungs observe a genuinely session-less visitor.
async function newIsolatedContext(app: { page: Page }): Promise<BrowserContext> {
  const browser = app.page.context().browser();
  if (!browser) {
    throw new Error('no browser handle for an isolated context');
  }
  return browser.newContext();
}
