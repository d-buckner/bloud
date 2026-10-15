// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { openAppFromHome } from '../lib/apps';
import { revealClientCredential, rotateClientCredential, waitForApp } from '../lib/api';
import { appUrlPattern } from '../lib/origin';

// One test case per observable behavior; serial mode (from describeApp) means
// the first failure skips the rungs behind it.
//
// Podly declares `sso.strategy: none`, and the two rungs that follow from that
// are the opposite of every other app's: the popup must land on Podly's own
// sign-in form rather than on Bloud's identity provider, and the credential that
// gets past it is the one Bloud minted, not an Authentik account. Together they
// are the browser-visible half of the design in apps/podly/INTEGRATION.md: the
// app keeps its own authentication because its feeds cannot follow an SSO
// redirect, and Bloud's job is to hold the password rather than to replace it.
describeApp('podly', (app) => {
  test('opens Podly own sign-in, not the identity provider', async () => {
    test.setTimeout(180_000);
    const podly = await openAppFromHome(app.page, 'Podly');
    try {
      // `strategy: none` means no forward-auth at the ingress: the document that
      // loads is Podly's, and the sign-in form on it is Podly's. A regression
      // that gated this app at the proxy would land on sso.localhost instead,
      // and this URL assertion is what names it.
      await expect(podly).toHaveURL(appUrlPattern('podly', '/login'), { timeout: 60_000 });
      await expect(podly.getByRole('heading', { name: /sign in to podly/i })).toBeVisible({
        timeout: 30_000,
      });
      // The app's own auth is on, which is the precondition for everything
      // below: with REQUIRE_AUTH off this route renders the library instead.
      await expect(podly.locator('#username')).toBeVisible();
      await expect(podly.locator('#password')).toBeVisible();
    } finally {
      await podly.close();
    }
  });

  test('signs in with the credential Bloud revealed', async () => {
    test.setTimeout(10 * 60_000);
    const password = await podlyPassword();

    const podly = await openAppFromHome(app.page, 'Podly');
    try {
      await podly.waitForURL(appUrlPattern('podly', '/login'), { timeout: 60_000 });
      await podly.locator('#username').fill('bloud');
      await podly.locator('#password').fill(password);
      await podly.getByRole('button', { name: /^sign in$/i }).click();

      // The app replaces the login route with the library on success, and sends
      // a failed sign-in back to the form with an error banner. Landing on `/`
      // is therefore the observable difference between the revealed password
      // being the one the app holds and being one it has never seen.
      await podly.waitForURL(appUrlPattern('podly', '/($|\\?)'), { timeout: 60_000 });
      await expect(podly.getByRole('heading', { name: /sign in to podly/i })).toHaveCount(0);
    } finally {
      await podly.close();
    }
  });
}, { label: 'Podly' });

/**
 * The password a person types into Podly, obtained the way an operator gets it.
 *
 * The reveal is single-use per value, and a suite run is not the only thing that
 * can spend it: `./bloud e2e lifecycle` re-runs Playwright against the same
 * runtime, and a retry re-runs the spec in a runtime where the first attempt
 * already revealed. So a 409 is not a failure here, it is the state a second run
 * finds itself in, and rotation is the only control that produces a value that
 * can be shown again. Rotation ends the app's sessions and forces the recreate
 * that carries the change, so the wait for `running` is part of taking it.
 */
async function podlyPassword(): Promise<string> {
  try {
    return (await revealClientCredential('podly', 'password')).value;
  } catch (err) {
    const message = err instanceof Error ? err.message : String(err);
    if (!message.includes('→ 409')) throw err;
    const rotated = await rotateClientCredential('podly', 'password');
    await waitForApp('podly', 'running', 10 * 60_000);
    return rotated.value;
  }
}
