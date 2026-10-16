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
    await this.page.waitForTimeout(this.settleMs);
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
}
