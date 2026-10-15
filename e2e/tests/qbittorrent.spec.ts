// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';

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
  (app) => {},
  {
    // `#desktop` only exists on the authenticated private page, never on the
    // public login page (`#loginform`).
    forwardAuth: {
      title: /qBittorrent/i,
      appShell: '#desktop',
    },
    label: 'qBittorrent',
  },
);
