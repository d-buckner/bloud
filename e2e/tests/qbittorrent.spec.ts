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
// qBittorrent is gated by forward-auth at the ingress, and Bloud's
// configurator whitelists the proxy's subnet inside the WebUI
// (AuthSubnetWhitelistEnabled + 0.0.0.0/0), so the container treats the
// forwarded request as authenticated instead of showing its own login
// form. Both halves are observable and both are asserted: the popup
// lands on the Authentik prompt (not on qBittorrent), and after that
// prompt is completed the popup reaches qBittorrent's own UI (`#desktop`,
// which only exists on the authenticated private page, never on the
// public login page) with no visible password field of its own: that
// last assertion is the proof the subnet whitelist took effect.
describeApp('qbittorrent', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs below are skipped, which distinguishes
    // a broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled('qbittorrent');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'qBittorrent');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'qBittorrent');
  });

  test('forward-auth gates the app: the popup lands on the login prompt', async () => {
    test.setTimeout(120_000);
    const qbittorrent = await openAppFromHome(app.page, 'qBittorrent');
    try {
      // A fresh popup has no qBittorrent session, so forward-auth must
      // intercept it: the popup round-trips to the Authentik flow on the
      // qBittorrent origin instead of serving the app.
      await expectForwardAuthPrompt(qbittorrent);
    } finally {
      await qbittorrent.close();
    }
  });

  test('signs in through forward-auth and reaches the qBittorrent UI', async () => {
    test.setTimeout(300_000);
    const qbittorrent = await openAppFromHome(app.page, 'qBittorrent');

    // Completing the prompt must serve qBittorrent's private UI: the
    // popup URL is the qBittorrent origin, the document title is
    // qBittorrent's, the static app shell (`#desktop`) rendered, and no
    // visible password field exists, proving the whitelisted request was
    // never sent to qBittorrent's own login page (`#loginform`).
    await signInThroughForwardAuth(qbittorrent, {
      origin: 'qbittorrent.localhost:8080',
      title: /qBittorrent/i,
      appShell: '#desktop',
    });
  });
});
