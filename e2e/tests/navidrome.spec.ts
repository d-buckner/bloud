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
// means the first failure skips the rungs behind it. Navidrome uses
// forward-auth: every request to the app origin is checked against
// Authentik before it reaches the container, so the popup opens on the
// Authentik prompt — the prompt appearing is itself the observable
// behavior of the auth rung, and completing it is the sign-in rung.
describeApp('navidrome', (app) => {
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

  test('forward-auth gates the app: the popup lands on the login prompt', async () => {
    test.setTimeout(120_000);
    const navidrome = await openAppFromHome(app.page, 'Navidrome');
    try {
      // A fresh popup has no Navidrome session, so forward-auth must
      // intercept: the popup round-trips to the Authentik flow on the
      // Navidrome origin instead of serving the app. The flow page is a
      // React app that renders its form after document load, so wait on
      // the form itself (redirect included) rather than checking once.
      const loginPage = new LoginPage(navidrome);
      await loginPage.usernameField.waitFor({
        state: 'visible',
        timeout: 60_000,
      });
    } finally {
      await navidrome.close();
    }
  });

  test('signs in through forward-auth and reaches the Navidrome UI', async () => {
    test.setTimeout(300_000);
    const navidrome = await openAppFromHome(app.page, 'Navidrome');

    // Complete the Authentik prompt. The flow can hop through
    // /if/flow/... steps that re-render after document load, so poll for
    // the form within a deadline instead of checking once.
    const loginPage = new LoginPage(navidrome);
    const deadline = Date.now() + 180_000;
    for (;;) {
      const url = navidrome.url();
      if (url.includes('navidrome.localhost:8080') && !url.includes('/if/flow'))
        break;
      if (Date.now() > deadline) break;

      if (await loginPage.isVisible()) {
        await loginPage.login();
        continue;
      }

      await navidrome.waitForTimeout(500);
    }

    // Forward-auth issued the session and Navidrome served the app.
    await expect(navidrome).toHaveURL(/navidrome\.localhost:8080/, {
      timeout: 30_000,
    });
    await expect(
      navidrome.locator('#root, .MuiBox-root, nav').first(),
    ).toBeVisible({ timeout: 30_000 });
  });
});
