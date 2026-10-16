// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test';

/**
 * Wait for a page title to match a pattern, or throw with the title it saw.
 *
 * A caller that needs a title match is usually settling after a redirect. This
 * polls instead of reading once, so a slow render does not read as a failure.
 */
export async function waitForTitle(
  page: Page,
  pattern: RegExp,
  timeoutMs: number,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let title = '';
  for (;;) {
    title = await page.title().catch(() => '');
    if (pattern.test(title)) return;
    if (Date.now() > deadline) {
      throw new Error(
        `page title never matched ${pattern}: still "${title}" at ${page.url()}`,
      );
    }
    await page.waitForTimeout(250);
  }
}

/**
 * Wait for a predicate over the page to become true, or throw with `label`.
 *
 * The harness avoids the Playwright `expect` surface on purpose: the library
 * must run inside the e2e runner and inside an ad-hoc recipe without loading a
 * second Playwright instance. Where the runner offers auto-retrying matchers,
 * this polls instead and reports the last value.
 */
export async function waitFor(
  label: string,
  predicate: () => Promise<boolean>,
  timeoutMs: number,
  intervalMs = 250,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    if (await predicate().catch(() => false)) return;
    if (Date.now() > deadline) {
      throw new Error(`${label} did not become true within ${timeoutMs}ms`);
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}
