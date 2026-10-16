// SPDX-License-Identifier: AGPL-3.0-only
import type { Locator } from '@playwright/test';
import { PageObject } from './base';
import { LoginPage } from '../login';
import { appOrigin, appUrlPattern } from '../origin';
import { CREDENTIALS } from '../config';

/**
 * Which screen of Home Assistant a page is sitting on.
 *
 * The sign-in route is a small state machine, and every one of its states is a
 * page a real user can be stuck on. Naming them is what lets a failure say
 * where it stopped instead of reporting a timeout.
 */
export type HaScreen = 'welcome' | 'idp-login' | 'consent' | 'dashboard' | 'unknown';

/**
 * Home Assistant, seen from a browser.
 *
 * Home Assistant renders its entire UI inside nested open shadow roots, so
 * ordinary CSS against the light DOM finds almost nothing and `innerText`
 * reads empty. Every locator here is either an attribute selector that
 * survives shadow piercing or a role query, which Playwright resolves through
 * shadow roots for us.
 */
export class HomeAssistantPage extends PageObject {
  /** hass-oidc-auth's provider welcome screen. */
  get loginWithBloudLink(): Locator {
    return this.page.locator('a[href="/auth/oidc/redirect"]');
  }

  /** Home Assistant's own legacy username/password form. */
  get legacyUsername(): Locator {
    return this.page.locator('input[name="username"]');
  }

  get legacyPassword(): Locator {
    return this.page.locator('input[type="password"]');
  }

  /**
   * The authenticated shell.
   *
   * `<hui-view>` is the marker that survived a panel-element rename upstream;
   * `ha-panel-lovelace` and `lovelace-ui` do not exist on the current Overview
   * dashboard, and polling for them is how this flow once burned its whole
   * timeout on a healthy install.
   */
  get dashboard(): Locator {
    return this.page.locator('hui-view');
  }

  /** The signed-in identity chip, which only renders for an authenticated user. */
  get userBadge(): Locator {
    return this.page.locator('ha-user-badge');
  }

  get continueButton(): Locator {
    return this.page.getByRole('button', { name: /Continue on this device/ });
  }

  /** The IdP's own submit control, once the flow reaches Authentik. */
  get idp(): LoginPage {
    return new LoginPage(this.page);
  }

  origin(): string {
    return appOrigin('homeassistant');
  }

  async open(): Promise<void> {
    await this.page.goto(this.origin() + '/', { waitUntil: 'domcontentloaded' });
    // An anonymous visitor is bounced to /auth/oidc/welcome immediately, so
    // wait for the redirect to land before anything reads the page.
    await this.page.waitForLoadState('load').catch(() => {});
    await this.page.waitForTimeout(1_500);
  }

  /** Classify the current screen without navigating anywhere. */
  async screen(): Promise<HaScreen> {
    if (await this.dashboard.first().isVisible().catch(() => false)) return 'dashboard';
    if (await this.loginWithBloudLink.first().isVisible().catch(() => false)) return 'welcome';
    if (await this.idp.isVisible()) return 'idp-login';
    if (await this.continueButton.first().isVisible().catch(() => false)) return 'consent';
    return 'unknown';
  }

  /**
   * Classify the current screen, letting a slow render finish first.
   *
   * `unknown` is the one classification that can be a lie: a dashboard that is
   * still hydrating over a remote TLS origin simply has not painted `hui-view`
   * yet. Declaring it unknown on first sight is how a correct flow reports a
   * phantom failure, so the classification is polled until it settles.
   */
  private async settle(timeoutMs = 30_000): Promise<HaScreen> {
    const deadline = Date.now() + timeoutMs;
    let screen = await this.screen();
    while (screen === 'unknown' && Date.now() < deadline) {
      await this.page.waitForTimeout(1_000);
      screen = await this.screen();
    }
    return screen;
  }

  /**
   * Walk the sign-in route to the dashboard.
   *
   * Returns the screen it stopped on so the caller can assert, rather than
   * throwing a generic timeout. The loop is bounded: a screen none of the
   * branches knows is reported as `unknown` with the URL, which is the
   * signal that a Home Assistant upgrade changed the route.
   */
  async signIn(
    username = CREDENTIALS.username,
    password = CREDENTIALS.password,
    maxSteps = 6,
  ): Promise<HaScreen> {
    for (let step = 0; step < maxSteps; step++) {
      const screen = await this.settle();
      if (screen === 'dashboard') return screen;
      if (screen === 'welcome') {
        await this.loginWithBloudLink.first().click();
      } else if (screen === 'idp-login') {
        await this.idp.login(username, password);
      } else if (screen === 'consent') {
        await this.continueButton.first().click();
      } else {
        return screen;
      }
      await this.page.waitForTimeout(1_000);
    }
    return this.settle();
  }

  /** Sign in with Home Assistant's own provider, bypassing the IdP entirely. */
  async signInWithPassword(username: string, password: string): Promise<void> {
    await this.legacyUsername.first().fill(username);
    await this.legacyPassword.first().fill(password);
    // The submit control is a `ha-progress-button`: the visible "Log in" label
    // lives inside its shadow root, so a light-DOM `button:has-text("Log in")`
    // never matches. A role query does.
    await this.page.getByRole('button', { name: /Log in/ }).first().click();
  }

  /** Assert the authenticated dashboard, then report what proved it. */
  async assertDashboard(): Promise<void> {
    await this.dashboard.first().waitFor({ state: 'visible', timeout: 60_000 });
    await this.userBadge.first().waitFor({ state: 'visible', timeout: 30_000 });
    if (/onboarding/.test(this.url)) {
      throw new Error(`Home Assistant is still in onboarding at ${this.url}`);
    }
    if (!appUrlPattern('homeassistant', '/(?!auth/)').test(this.url)) {
      throw new Error(`Home Assistant settled off its own origin: ${this.url}`);
    }
  }
}
