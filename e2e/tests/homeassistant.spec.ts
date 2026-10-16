// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import { describeApp } from '../lib/app-suite';
import { HomeAssistantPage, appUrlPattern, openAppFromHome } from '@bloud/browser-harness';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Home Assistant uses
// native-oidc via the hass-oidc-auth custom integration: the auth gate
// lives inside the app (the popup round-trips through the IdP and the
// provider's own welcome/consent screens), so the observable gate is "the
// popup shows an auth screen instead of the dashboard".
describeApp('homeassistant', (app) => {
  test('SSO gates the app: the popup lands on an auth screen', async () => {
    test.setTimeout(120_000);
    const ha = new HomeAssistantPage(await openAppFromHome(app.page, 'Home Assistant'));
    try {
      // A fresh popup has no Home Assistant session. Between the popup
      // and the dashboard sit the Authentik identifier-first login (when
      // the IdP session did not carry over) and the hass-oidc-auth
      // provider welcome screen ("Login with Bloud"); either is proof
      // the gate held. An unauthenticated browser can never reach the
      // Lovelace shell, so failing to render it while an auth screen
      // renders is the assertion.
      const authScreen = ha.loginWithBloudLink.or(ha.idp.usernameField);
      await expect(authScreen).toBeVisible({ timeout: 60_000 });
      // The gate held: no Lovelace view is rendered behind the auth screen.
      await expect(ha.dashboard).toHaveCount(0);
      expect(ha.url).toMatch(
        // Either the app's own origin or the shared issuer: both are the gate
        // holding. Built from the origin (protocol included) rather than the bare
        // host, since a URL starts with the scheme and an anchor on the hostname
        // alone would never match.
        new RegExp(`${appUrlPattern('homeassistant').source}|sso\\.localhost`),
      );
    } finally {
      await ha.page.close();
    }
  });

  test('signs in through OIDC and reaches the dashboard', async () => {
    // Measured against a live instance: the whole flow is ~20 s. The previous
    // 6-minute budget existed because this test polled `ha-panel-lovelace,
    // lovelace-ui` as its "we are in" marker, and neither element exists on the
    // current Overview dashboard, so the loop could only ever exit through its
    // deadline. Keep the budget tight enough that a future selector rot fails
    // fast instead of hiding as slowness.
    test.setTimeout(120_000);
    const ha = new HomeAssistantPage(await openAppFromHome(app.page, 'Home Assistant'));

    // Home Assistant completed first-run onboarding during convergence (the
    // app's PostStart does it), so no onboarding wizard appears here; if one
    // ever does, this test should fail rather than quietly click through it.
    //
    // The walk itself lives in HomeAssistantPage.signIn so that a hand-run
    // probe (tests/diagnose.spec.ts) walks the identical route: a probe that
    // reaches a different screen than the gate test reaches is worse than no
    // probe, because it argues about selectors instead of the app.
    const stopped = await ha.signIn();
    expect(stopped, `sign-in stopped on "${stopped}" at ${ha.url}`).toBe('dashboard');

    // Terminal state: the authenticated dashboard, plus the sidebar identity
    // chip (display-name claim from the IdP), which proves the account came
    // from Authentik rather than Home Assistant's built-in legacy provider.
    await ha.assertDashboard();
  });
}, { label: 'Home Assistant' });
