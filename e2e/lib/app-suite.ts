// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test } from '@playwright/test';
import type { BrowserContext, Page } from '@playwright/test';
import { ensureSignedIn } from './auth';

export interface AppSuite {
  readonly name: string;
  /**
   * Signed-in Bloud page on the Traefik origin, shared by every test in
   * the block. Assigned while the `beforeAll` hook runs — read `app.page`
   * inside test callbacks, never at collection time.
   */
  page: Page;
}

/**
 * A real `test.describe` for one app, pre-configured with the plumbing
 * every app spec needs:
 *
 *  - `mode: 'serial'`: the first failed test skips the ones after it, so
 *    one broken stage doesn't cascade into noise.
 *  - One signed-in Bloud page shared by all tests in the block. Sign-ins
 *    into the app itself happen in popups, so the Bloud session is never
 *    mutated mid-suite and the Authentik login happens exactly once.
 *
 * Test cases stay explicit. Order them so the rungs a broken install
 * invalids come first — the convention is a "converges to running" test
 * (via `ensureInstalled`) first, then UI behavior:
 *
 *   describeApp('jellyfin', (app) => {
 *     test('converges to running', async () => {
 *       test.setTimeout(12 * 60_000);
 *       await ensureInstalled('jellyfin');
 *     });
 *
 *     test('appears on the home screen', async () => {
 *       await app.page.goto('/');
 *       ...
 *     });
 *   });
 */
export function describeApp(name: string, body: (app: AppSuite) => void): void {
  test.describe(name, () => {
    test.describe.configure({ mode: 'serial' });

    const app = { name } as AppSuite;
    let context: BrowserContext;

    test.beforeAll(async ({ browser }) => {
      // The hook only creates the context and completes the Authentik
      // login; a failure here is environment/auth, not app behavior.
      test.setTimeout(5 * 60_000);
      context = await browser.newContext();
      app.page = await context.newPage();
      await ensureSignedIn(app.page);
    });

    test.afterAll(async () => {
      await context?.close();
    });

    body(app);
  });
}
