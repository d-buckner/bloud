// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Navidrome uses
// forward-auth: every request to the app origin is checked against
// Authentik before it reaches the container, so the popup opens on the
// Authentik prompt: the prompt appearing is itself the observable
// behavior of the auth rung, and completing it is the sign-in rung. Both
// are the framework's forward-auth rungs.
describeApp(
  'navidrome',
  (app) => {},
  {
    forwardAuth: {
      title: /Navidrome/i,
      appShell: '#root, .MuiBox-root, nav',
    },
    label: 'Navidrome',
  },
);
