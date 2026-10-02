// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '@playwright/test';
import type { BrowserContext, Page } from '@playwright/test';
import { ensureSignedIn } from './auth';
import { openAppFromHome } from './apps';
import { expectForwardAuthGate, signInThroughForwardAuth } from './forwardAuth';
import { appHost } from './origin';

export interface AppSuite {
  readonly name: string;
  /**
   * Signed-in Bloud page on the Traefik origin, shared by every test in
   * the block. Assigned while the `beforeAll` hook runs: read `app.page`
   * inside test callbacks, never at collection time.
   */
  page: Page;
}

/**
 * The app-specific values the framework's forward-auth rungs need. The
 * mechanism is shared; only these values vary per app.
 */
export interface ForwardAuthRungs {
  /** The home-screen tile label, e.g. "qBittorrent". */
  label: string;
  /** The app document's title pattern, asserted after sign-in. */
  title: RegExp;
  /** Rendered-content selector that proves the app's own UI mounted. */
  appShell: string;
}

export interface DescribeAppOptions {
  /**
   * Register the standard forward-auth rungs for an app gated at the
   * ingress. The rungs belong to the framework, not the spec:
   *
   *  - the gate rung needs its own browser context, because the suite's
   *    shared context is signed into Authentik and the outpost can
   *    authorize a valid session without showing a prompt, which is
   *    correct behavior but fails an assertion that always expects one;
   *  - the sign-in rung is the same journey for every forward-auth app.
   *
   * The spec supplies only the app-specific values above. The rungs are
   * registered after `body`, so the serial suite runs the pre-auth rungs
   * first and the app is proven reachable before the gate is exercised.
   */
  forwardAuth?: ForwardAuthRungs;
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
 * invalids come first: the convention is a "converges to running" test
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
 *
 * A forward-auth app passes `{ forwardAuth: { label, title, appShell } }`
 * and gets the two ingress rungs registered for it, instead of copying
 * the same two `test(...)` blocks into every spec.
 */
export function describeApp(
  name: string,
  body: (app: AppSuite) => void,
  options: DescribeAppOptions = {},
): void {
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

    // App-specific rungs first: serial mode skips the auth rungs when the
    // install or the tile is broken, which is the failure it names.
    body(app);

    if (options.forwardAuth) {
      registerForwardAuthRungs(name, app, options.forwardAuth);
    }
  });
}

/**
 * The two rungs every forward-auth app asserts. Registered by
 * `describeApp`, never copied into a spec.
 */
function registerForwardAuthRungs(
  name: string,
  app: AppSuite,
  rungs: ForwardAuthRungs,
): void {
  test('forward-auth gates the app: the popup lands on the login prompt', async ({
    browser,
  }) => {
    test.setTimeout(120_000);
    await expectForwardAuthGate(browser, name);
  });

  test(`signs in through forward-auth and reaches the ${rungs.label} UI`, async () => {
    test.setTimeout(300_000);
    const popup = await openAppFromHome(app.page, rungs.label);
    await signInThroughForwardAuth(popup, {
      origin: appHost(name),
      title: rungs.title,
      appShell: rungs.appShell,
    });
  });
}
