// SPDX-License-Identifier: AGPL-3.0-only
import { expect, type Page } from '@playwright/test';
import { LoginPage } from './loginPage';

/**
 * Shared journey for apps that are gated by Authentik's forward-auth
 * middleware (Sonarr, Radarr, Prowlarr, qBittorrent, and Navidrome,
 * which these rungs are modeled on): every request to the app origin is
 * checked against the IdP *before* it reaches the container, so a fresh
 * popup is redirected to the Authentik prompt and only receives the app
 * document once the flow completes.
 *
 * The two halves of that arrangement are separate observable behaviors,
 * so they are separate helpers and the specs keep one test case each:
 *
 *  - `expectForwardAuthPrompt` asserts the ingress gate: the popup never
 *    reached the app, it is sitting on the Authentik login form.
 *  - `signInThroughForwardAuth`: completing that prompt serves the app
 *    itself, and the app does not ask for its own credentials.
 *
 * App-specific assertions stay in the specs (each passes the origin,
 * title pattern and app-shell selector it owns), so a red run names the
 * rung and the app that broke rather than a generic failure.
 */

/** The app-specific inputs a spec supplies for its own sign-in rung. */
export interface ForwardAuthSignIn {
  /** The app's own origin, e.g. `sonarr.localhost:8080`. */
  origin: string;
  /** Pattern the app's document title must match once it is served. */
  title: RegExp;
  /**
   * Selector that only exists once the app's own UI is rendering. Used
   * to settle the page before the credential assertion below: checking
   * for a password field on a half-rendered document would pass
   * vacuously.
   *
   * Must be a *rendered content* signal, not merely a mount container:
   * the Servarr apps ship `<div id="root">` empty in the static HTML and
   * only populate it after the app answers `initialize.json`, so their
   * specs pass `#root:not(:empty)`. qBittorrent's `#desktop` is static
   * markup that exists only on the authenticated page, so it is fine
   * as-is.
   */
  appShell: string;
}

/** Build a RegExp matching a literal origin string. */
function originPattern(origin: string): RegExp {
  return new RegExp(origin.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
}

/**
 * Assert the app is gated: a popup opened against the app origin has no
 * app session, so forward-auth intercepts it and the popup round-trips
 * to Authentik's flow *on the app's origin* instead of serving the app.
 * The flow page is a React app that renders its form after document
 * load, so wait on the form itself (redirect included).
 */
export async function expectForwardAuthPrompt(popup: Page): Promise<void> {
  const loginPage = new LoginPage(popup);
  await loginPage.usernameField.waitFor({ state: 'visible', timeout: 60_000 });
}

/**
 * Complete the Authentik prompt and verify the app was served, then
 * assert the app did not demand credentials of its own: the app's auth
 * strategy (Servarr: `AuthenticationMethod=External`; qBittorrent: the
 * subnet whitelist) means a signed-in forward-auth request is already
 * authenticated as far as the container is concerned.
 *
 * No assertion is made about *which* login each spec's app would
 * normally need; a failure here means the ingress gate, the app's own
 * provisioned auth, or the app's boot, and the failing step says which.
 */
export async function signInThroughForwardAuth(
  popup: Page,
  options: ForwardAuthSignIn,
): Promise<void> {
  const loginPage = new LoginPage(popup);
  const origin = originPattern(options.origin);
  const shell = popup.locator(options.appShell).first();
  const passwordField = popup.locator('input[type="password"]:visible').first();

  // Poll for the end state rather than for a URL: the flow hops through
  // `/<app origin>/outpost.goauthentik.io/...` and `/if/flow/...` pages
  // that re-render after document load, so any "are we on the app yet"
  // shortcut can fire mid-redirect while the prompt is still due. The
  // only unambiguous signals are the app's own UI rendering and the
  // app's own login form appearing, and the loop exits on either.
  const deadline = Date.now() + 180_000;
  for (;;) {
    // Done: the app's own UI rendered at last.
    if (await shell.isVisible().catch(() => false)) break;

    // The app answered with its own login form instead. Under Bloud's
    // provisioning this must never happen, so stop waiting and let the
    // assertions below report it precisely. Only the app's *document*
    // counts: the IdP's own prompt also has a password field, and the
    // forward-auth start page shares the app origin.
    const url = popup.url();
    const servedByApp = origin.test(url) && !url.includes('outpost.goauthentik.io');
    if (servedByApp && (await passwordField.isVisible().catch(() => false)))
      break;

    if (Date.now() > deadline) break;

    if (await loginPage.isVisible()) {
      await loginPage.login();
      continue;
    }

    await popup.waitForTimeout(500);
  }

  // (a) Forward-auth issued the session and the ingress stopped
  // redirecting: the popup now sits on the app's own origin.
  await expect(popup).toHaveURL(origin, { timeout: 30_000 });
  // (b) What is served there is the app itself, not a proxy or error page.
  await expect(popup).toHaveTitle(options.title, { timeout: 30_000 });
  // (c) The app's UI rendered ...
  await expect(shell).toBeVisible({ timeout: 60_000 });
  // ... and it did not ask for its own credentials. A visible password
  // field here is the app having fallen back to its own login form,
  // which under Bloud's provisioning must never happen.
  await expect(popup.locator('input[type="password"]:visible')).toHaveCount(0);
}
