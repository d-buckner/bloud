// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test';
import { Diagnostics, visibleText } from './diagnostics';

/**
 * Base for every page object.
 *
 * A page object owns two things and nothing else: the locators that identify
 * its screens, and the actions a user takes on them. Assertions stay in the
 * caller, because the same screen is asserted differently by a gate test and
 * by a sign-in test, and a page object that asserts hides which of the two
 * failed.
 *
 * The same instances are used by the Playwright specs and by a hand-run probe
 * (`tests/diagnose.spec.ts`). That is the whole reason this layer exists: a
 * manual look and an automated gate must not disagree about what a selector
 * means, which is how a hand probe once declared a working Authentik page
 * blank because it read `innerText` across a shadow boundary.
 */
export class PageObject {
  readonly diag: Diagnostics;

  constructor(readonly page: Page) {
    this.diag = new Diagnostics(page);
  }

  /** Where the browser is right now. */
  get url(): string {
    return this.page.url();
  }

  /** Rendered text, shadow roots included. */
  text(): Promise<string> {
    return visibleText(this.page);
  }

  title(): Promise<string> {
    return this.page.title();
  }

  /** Screenshot under a stable name so a probe leaves readable evidence. */
  async shot(name: string): Promise<string> {
    const path = `/tmp/pom-${name}.png`;
    await this.page.screenshot({ path });
    return path;
  }

  /** One-line summary of what this page is, for a probe transcript. */
  async describe(): Promise<string> {
    const text = (await this.text()).replace(/\s+/g, ' ').slice(0, 200);
    return `${await this.title()}  |  ${this.url.slice(0, 120)}  |  "${text}"`;
  }
}
