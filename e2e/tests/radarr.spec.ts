// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it.
//
// Radarr is gated by forward-auth at the ingress, and Bloud's
// configurator provisions its config.xml with AuthenticationMethod
// External, so the container treats an already-authenticated request as
// authenticated and never shows its own login form. Both halves are the
// framework's forward-auth rungs: the popup lands on the Authentik prompt
// (not on Radarr), and after that prompt is completed Radarr serves its
// own UI with no visible password field of its own.
describeApp(
  'radarr',
  (app) => {},
  {
    // `#root:not(:empty)`: the mount div ships empty and is only filled
    // once the app answers `initialize.json`, and Forms auth would instead
    // serve the separate login page with no `#root` at all.
    forwardAuth: {
      title: /Radarr/i,
      appShell: '#root:not(:empty)',
    },
    label: 'Radarr',
  },
);
