// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';

const AFFINE_URL = 'http://affine.localhost:8080';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. AFFiNE uses
// native-oidc with a user-initiated flow: unlike Immich (whose login
// page auto-launches OIDC), unauthenticated AFFiNE renders its editor
// with a "Sign in and enable" affordance — the observable gate is that
// this sign-in path launches Bloud's OIDC provider on sso.localhost and
// settles on the Authentik prompt. The auth rungs open their own fresh
// browser contexts: AFFiNE's pre-sign-in workspace state lives in the
// browser profile, so a shared context would make them order-dependent.
describeApp('affine', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull (own postgres + redis +
    // server) + first-run convergence. When this fails, the UI rungs
    // below are skipped, which distinguishes a broken install from a
    // misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('affine');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'AFFiNE');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'AFFiNE');
  });

  test('SSO gates the app: sign-in launches the OIDC flow to the prompt', async ({
    browser,
  }) => {
    test.setTimeout(180_000);
    // AFFiNE's pre-sign-in workspace state persists in the browser
    // profile (IndexedDB) and launching the OIDC flow mutates it, so the
    // sign-in affordance is one-shot per profile: this rung needs its own
    // fresh context, not the shared suite one. The tile launch itself is
    // rung 3's contract; the auth rungs go straight to the app origin.
    const context = await browser.newContext();
    try {
      const affine = await context.newPage();
      await affine.goto(AFFINE_URL);
      await launchOidcFlow(affine);
      // The flow round-trips to the issuer (sso.localhost), where this
      // context has no session, so it must settle on the Authentik login
      // form.
      await affine.waitForURL(/sso\.localhost/, { timeout: 60_000 });
      const loginPage = new LoginPage(affine);
      await loginPage.usernameField.waitFor({
        state: 'visible',
        timeout: 30_000,
      });
    } finally {
      await context.close();
    }
  });

  test('signs in through OIDC and reaches the workspace', async ({
    browser,
  }) => {
    test.setTimeout(360_000);
    // Same fresh-profile requirement as the gate rung above.
    const context = await browser.newContext();
    try {
      const affine = await context.newPage();
      await affine.goto(AFFINE_URL);

      await launchOidcFlow(affine);

      // Complete the Authentik login on the issuer origin. The flow can
      // hop through /if/flow/... steps that re-render after document
      // load, so poll for the form within a deadline instead of checking
      // once.
      const loginPage = new LoginPage(affine);
      const deadline = Date.now() + 240_000;
      for (;;) {
        if (affine.url().includes('/workspace/')) break;
        if (Date.now() > deadline) break;

        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }

        await affine.waitForTimeout(500);
      }

      // AFFiNE exchanges the code at /oauth/callback, creates the app
      // account on first login (matched by email), and redirects into
      // the user's workspace.
      await expect(affine).toHaveURL(/\/workspace\//, { timeout: 120_000 });

      // The signed-in shell no longer offers the sign-in affordance.
      await expect(
        affine.getByRole('button', { name: /sign in and enable/i }),
      ).toBeHidden({ timeout: 30_000 });
    } finally {
      await context.close();
    }
  });
});

/**
 * Drive AFFiNE's unauthenticated UI to the point where it redirects to
 * the OIDC provider: the local editor is read-only until sign-in and
 * shows a "Sign in and enable" button; clicking it opens the sign-in
 * modal, and "Continue with OIDC" starts the flow. The AFFiNE bundle is
 * large, so wait generously for the shell before clicking.
 */
async function launchOidcFlow(affine: Page): Promise<void> {
  const signInButton = affine
    .getByRole('button', { name: /sign in/i })
    .first();
  await signInButton.waitFor({ state: 'visible', timeout: 120_000 });
  await signInButton.click();

  const oidcButton = affine.getByRole('button', {
    name: /continue with oidc/i,
  });
  await oidcButton.waitFor({ state: 'visible', timeout: 30_000 });
  await oidcButton.click();
}
