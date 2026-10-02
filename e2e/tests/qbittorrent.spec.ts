// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { expectInstalledInCatalog, expectRunningTile } from '../lib/apps';
import { ensureInstalled } from '../lib/api';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// qBittorrent is gated by forward-auth at the ingress, and Bloud's
// configurator whitelists the proxy's subnet inside the WebUI
// (AuthSubnetWhitelistEnabled + 0.0.0.0/0), so the container treats the
// forwarded request as authenticated instead of showing its own login
// form. Both halves are the framework's forward-auth rungs: the popup
// lands on the Authentik prompt (not on qBittorrent), and after that
// prompt is completed the popup reaches qBittorrent's own UI with no
// visible password field of its own, which is the proof the subnet
// whitelist took effect.
describeApp(
  'qbittorrent',
  (app) => {
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
  },
  {
    // `#desktop` only exists on the authenticated private page, never on the
    // public login page (`#loginform`).
    forwardAuth: {
      label: 'qBittorrent',
      title: /qBittorrent/i,
      appShell: '#desktop',
    },
  },
);
