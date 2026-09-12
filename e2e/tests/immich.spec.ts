// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Immich uses
// native-oidc: unlike forward-auth, the gate lives inside the app — the
// login page auto-launches the OIDC flow, which round-trips through
// Authentik on the issuer host (sso.localhost), where this context has no
// session, so the flow settles on the login prompt.
describeApp('immich', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence
    // (DB migrations + geodata import). When this fails, the UI rungs
    // below are skipped, which distinguishes a broken install from a
    // misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('immich');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Immich');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Immich');
  });

  test('SSO auto-launches the OIDC flow to the Authentik prompt', async () => {
    test.setTimeout(120_000);
    const immich = await openAppFromHome(app.page, 'Immich');
    try {
      // With SSO enabled, Immich redirects to its OIDC provider without
      // any user interaction. The context is signed in to Bloud on
      // localhost:8080, but the issuer is sso.localhost — a different
      // cookie scope — so the flow must land on the Authentik prompt.
      const loginPage = new LoginPage(immich);
      await loginFormVisible(loginPage, immich);
      expect(immich.url()).toContain('sso.localhost');
    } finally {
      await immich.close();
    }
  });

  test('signs in through OIDC and reaches the photos page', async () => {
    // Cold starts (first OIDC round-trip after install) can be slow, so
    // poll the whole flow within a generous deadline.
    test.setTimeout(360_000);
    const immich = await openAppFromHome(app.page, 'Immich');

    // First-time SSO users are created on login and walk Immich's
    // onboarding wizard before reaching the photos page; repeat runs
    // skip it. Click through whatever appears until the flow lands.
    const loginPage = new LoginPage(immich);
    const nextButton = immich.locator('#onboarding-card button').last();
    const deadline = Date.now() + 300_000;
    for (;;) {
      const url = immich.url();
      if (url.includes('/photos')) break;
      if (Date.now() > deadline) break;

      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }

      // Onboarding wizard: click through each step until it completes.
      if (await nextButton.isVisible({ timeout: 1_000 }).catch(() => false)) {
        await nextButton.click();
        await immich.waitForTimeout(500);
        continue;
      }

      await immich.waitForTimeout(500);
    }

    // The OIDC callback lands back on Immich, which exchanges the code
    // and redirects authenticated users to the photos page.
    await expect(immich).toHaveURL(/immich\.localhost:8080\/photos/, {
      timeout: 60_000,
    });
    // The app shell must render for the signed-in user: the top navbar
    // (id dashboard-navbar) only exists for authenticated sessions (the
    // mobile navbar is hidden at desktop widths).
    await expect(immich.locator('nav#dashboard-navbar')).toBeVisible({
      timeout: 30_000,
    });
  });
});

/**
 * Wait for the Authentik login form to render. The flow passes through
 * /if/flow/... pages that re-render after document load, so poll for the
 * form itself rather than checking once after navigation.
 */
async function loginFormVisible(
  loginPage: LoginPage,
  page: { waitForTimeout(ms: number): Promise<void> },
): Promise<void> {
  const deadline = Date.now() + 60_000;
  for (;;) {
    if (await loginPage.isVisible()) return;
    if (Date.now() > deadline) {
      throw new Error('OIDC flow did not reach the Authentik login prompt');
    }
    await page.waitForTimeout(500);
  }
}
