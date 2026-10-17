// SPDX-License-Identifier: AGPL-3.0-only
import { mkdir } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import type { Page } from '@playwright/test';
import { ensureSignedIn } from './auth';
import { Diagnostics, visibleText, type Problem } from './diagnostics';
import { appOrigin } from './origin';

/** What a capture leaves behind: the page's own words and what it broke. */
export interface SessionReview {
  url: string;
  title: string;
  text: string;
  problems: Problem[];
  pending: string[];
}

export interface HarnessSessionOptions {
  /** Sign in before a capture. Default true. */
  signedIn?: boolean;
  /** Time to let the page settle after a navigation, in milliseconds. Default 1500. */
  settleMs?: number;
  /**
   * True when this session created its own browser context, so `close` may close
   * the context and not only the page. `fork` sets it; a borrowed page never has it.
   */
  ownsContext?: boolean;
}

/** A filesystem-safe token for one target, for a screenshot file name. */
function slug(value: string): string {
  return value.replace(/^https?:\/\//, '').replace(/[^a-zA-Z0-9._-]+/g, '-').replace(/^-|-$/g, '');
}

/**
 * A page driven by the harness, with the Bloud platform knowledge attached.
 *
 * The e2e suite constructs one inside a spec. The harness CLI constructs one
 * from the Playwright `page` fixture. Both then read the same diagnostics and
 * take the same path to a signed-in page, which is what keeps an ad-hoc look
 * and the gate suite from disagreeing about the UI.
 */
export class HarnessSession {
  readonly diag: Diagnostics;

  constructor(
    readonly page: Page,
    private readonly options: HarnessSessionOptions = {},
  ) {
    this.diag = new Diagnostics(page);
  }

  get signedIn(): boolean {
    return this.options.signedIn !== false;
  }

  get settleMs(): number {
    return this.options.settleMs ?? 1_500;
  }

  /** Sign in on the Bloud origin. No-ops when a session already exists. */
  async signIn(): Promise<void> {
    await ensureSignedIn(this.page);
  }

  /**
   * Open a target. `bloud` opens the dashboard, a bare app id opens that app's
   * origin, and anything with a scheme is used as given.
   */
  async open(target: string): Promise<void> {
    const url = /^(https?|file):\/\//.test(target)
      ? target
      : target === 'bloud'
        ? '/'
        : appOrigin(target);
    await this.page.goto(url, { waitUntil: 'domcontentloaded' });
    await this.settle();
    await this.page.waitForTimeout(this.settleMs);
  }

  /**
   * Wait until the page has stopped moving.
   *
   * A fixed sleep after a navigation is the wrong wait twice over: too short for
   * an app that bounces a visitor through the identity provider and back, and
   * wasted on a page that renders at once. This waits for the requests to drain,
   * then for the address to hold still, and gives up on a budget rather than
   * forever, because an app that holds a stream open never goes quiet at all.
   */
  async settle(timeoutMs = 8_000): Promise<void> {
    await this.page.waitForLoadState('networkidle', { timeout: timeoutMs }).catch(() => {});
    const deadline = Date.now() + timeoutMs;
    let last = this.page.url();
    let stableSince = Date.now();
    while (Date.now() < deadline) {
      await this.page.waitForTimeout(250);
      const url = this.page.url();
      if (url !== last) {
        last = url;
        stableSince = Date.now();
        continue;
      }
      if (Date.now() - stableSince >= 1_000) return;
    }
  }

  /**
   * Capture one screenshot and return its path.
   *
   * The directory defaults to `BLOUD_HARNESS_OUT`, which the CLI sets to
   * `<repo>/.bloud/screenshots`. An explicit `outDir` wins.
   */
  async screenshot(
    name: string,
    options: { fullPage?: boolean; outDir?: string } = {},
  ): Promise<string> {
    const dir = resolve(options.outDir ?? process.env.BLOUD_HARNESS_OUT ?? '.');
    await mkdir(dir, { recursive: true });
    const path = join(dir, `${new Date().toISOString().replace(/[:.]/g, '-')}-${slug(name)}.png`);
    await this.page.screenshot({ path, fullPage: options.fullPage ?? false });
    return path;
  }

  /** What the page says and what it broke, for a review or a failure report. */
  async review(): Promise<SessionReview> {
    return {
      url: this.page.url(),
      title: await this.page.title().catch(() => ''),
      text: await visibleText(this.page).catch(() => ''),
      problems: this.diag.problems_,
      pending: this.diag.pending,
    };
  }

  /**
   * A second page in a browser context of its own, signed in or anonymous.
   *
   * A check that needs an anonymous look cannot take it on the run's own page:
   * that page holds the Authentik session, and signing out of it signs out of
   * everything behind it. The caller closes the returned session when it is done.
   */
  async fork(options: { signedIn?: boolean } = {}): Promise<HarnessSession> {
    const browser = this.page.context().browser();
    if (!browser) {
      throw new Error('this page has no browser to fork a context from');
    }
    const context = await browser.newContext();
    const page = await context.newPage();
    const forked = new HarnessSession(page, {
      signedIn: options.signedIn ?? true,
      settleMs: this.settleMs,
      ownsContext: true,
    });
    if (forked.signedIn) {
      await forked.signIn();
    }
    return forked;
  }

  /** Close this session's page, and its context when the session created it. */
  async close(): Promise<void> {
    if (this.options.ownsContext) {
      await this.page.context().close().catch(() => {});
      return;
    }
    await this.page.close().catch(() => {});
  }
}
