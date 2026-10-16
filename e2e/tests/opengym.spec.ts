// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { openAppFromHome } from '../lib/apps';
import { appUrlPattern } from '../lib/origin';

// openGym declares `sso.strategy: none`, and these rungs are the browser-visible half
// of the reason: the app signs its own users in with a passkey, and a passkey is bound
// to the exact hostname the browser is on, so Bloud's job is to tell the app which
// origin it is, not to stand in front of it.
//
// The last rung is the one that cannot be faked from outside a browser. The app
// compares each same-origin request against ORIGIN and refuses a mismatch, and only a
// real navigation sends the header it compares. A profile created here is proof that
// the address file reached the container, that nginx resolved the API name, and that
// the write landed on the mounted data directory.
describeApp('opengym', (app) => {
  test('opens openGym own sign-in, not the identity provider', async () => {
    test.setTimeout(180_000);
    const gym = await openAppFromHome(app.page, 'openGym');
    try {
      // `strategy: none` means no gate at the ingress: the document that loads is
      // openGym's. A regression that gated this app at the proxy would land on
      // sso.localhost instead, and this URL assertion is what names it.
      await expect(gym).toHaveURL(appUrlPattern('opengym', '/'), { timeout: 60_000 });
      await expect(gym.getByText(/Your workouts\. Your weights\./)).toBeVisible({
        timeout: 30_000,
      });
    } finally {
      await gym.close();
    }
  });

  test('offers the password sign-in Bloud turns on', async () => {
    test.setTimeout(120_000);
    const gym = await openAppFromHome(app.page, 'openGym');
    try {
      await expect(gym).toHaveURL(appUrlPattern('opengym', '/'), { timeout: 60_000 });
      // The login page reads `password_login` from /api/config to decide whether this
      // button exists at all. Seeing it means the value travelled from metadata.yaml
      // through the container environment into the app's own config document, and it
      // is the only way in on a plain-http deployment reached by hostname, where
      // WebAuthn refuses to run.
      await expect(gym.getByRole('button', { name: /sign in with password/i })).toBeVisible({
        timeout: 30_000,
      });
    } finally {
      await gym.close();
    }
  });

  test('creates a profile through the app and reaches its tab bar', async () => {
    test.setTimeout(180_000);
    const gym = await openAppFromHome(app.page, 'openGym');
    try {
      await expect(gym).toHaveURL(appUrlPattern('opengym', '/'), { timeout: 60_000 });
      await gym.getByRole('button', { name: /sign in with password/i }).click();

      // A run-unique name, because registration is open on a fresh install and
      // `./bloud e2e lifecycle` runs this spec twice against one runtime. The app
      // refuses a name two profiles already share.
      await gym.getByPlaceholder('Your name').fill(`E2E ${Date.now()}`);
      await gym.getByPlaceholder(/passphrase/).fill('bloud e2e passphrase');
      await gym.getByRole('button', { name: /create account/i }).click();

      // The login overlay is replaced by the app on success, and the tab bar is what
      // an unauthenticated visitor never sees. A refused origin or a proxy that never
      // reached the API leaves the tagline on screen instead.
      await expect(gym.getByRole('button', { name: 'Exercises' })).toBeVisible({
        timeout: 60_000,
      });
    } finally {
      await gym.close();
    }
  });
}, {
  label: 'openGym',
  // Two images, about 0.48 GB unpacked between them. The web image carries the exercise
  // stills and animations, so there is no second download after the containers are up.
  convergeTimeoutMs: 360_000,
});
