// SPDX-License-Identifier: AGPL-3.0-only
import { test } from '@playwright/test';
import type { BrowserContext, Page } from '@playwright/test';
import { ensureSignedIn } from './auth';
import { ensureInstalled } from './api';
import { expectInstalledInCatalog, expectRunningTile, openAppFromHome } from './apps';
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
   * The display name the dashboard shows for this app, for example
   * "qBittorrent". The generic rungs look the app up by that label, because
   * that is what a person reads on the tile and on the catalog card.
   */
  label: string;
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
  forwardAuth?: Omit<ForwardAuthRungs, 'label'>;
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
 * It also registers the three rungs that say nothing about the app: the
 * install converges, the app shows in the catalog, and the home tile
 * reaches RUNNING. Those were copied into all sixteen app specs, and a
 * copy is a thing that can be missing in one spec and right in fifteen.
 * They run first, so serial mode skips everything behind them when the
 * install itself is broken, which names the real failure instead of
 * reporting fifteen UI timeouts.
 *
 * What is left in a spec is only what that app does:
 *
 *   describeApp('jellyfin', (app) => {
 *     test('opens from the home tile and serves its login page', async () => {
 *       ...
 *     });
 *   }, { label: 'Jellyfin' });
 *
 * A forward-auth app adds `forwardAuth: { title, appShell }` and gets the
 * two ingress rungs registered for it, instead of copying the same two
 * `test(...)` blocks into every spec.
 */
export function describeApp(
  name: string,
  body: (app: AppSuite) => void,
  options: DescribeAppOptions,
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

    registerConvergenceRungs(name, options.label, app);

    // App-specific rungs next: serial mode skips the auth rungs when the
    // install or the tile is broken, which is the failure it names.
    body(app);

    if (options.forwardAuth) {
      registerForwardAuthRungs(name, options.label, app, options.forwardAuth);
    }
  });
}

/**
 * The three rungs every installed app owes the dashboard, registered by
 * `describeApp`, never copied into a spec. They are ordered so the rung a
 * broken install invalids comes first.
 */
function registerConvergenceRungs(name: string, label: string, app: AppSuite): void {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs behind it are skipped, which
    // distinguishes a broken install from a misbehaving app.
    test.setTimeout(12 * 60_000);
    await ensureInstalled(name);
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, label);
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, label);
  });
}

/**
 * The two rungs every forward-auth app asserts. Registered by
 * `describeApp`, never copied into a spec.
 */
function registerForwardAuthRungs(
  name: string,
  label: string,
  app: AppSuite,
  rungs: Omit<ForwardAuthRungs, 'label'>,
): void {
  test('forward-auth gates the app: the popup lands on the login prompt', async ({
    browser,
  }) => {
    test.setTimeout(120_000);
    await expectForwardAuthGate(browser, name);
  });

  test(`signs in through forward-auth and reaches the ${label} UI`, async () => {
    test.setTimeout(300_000);
    const popup = await openAppFromHome(app.page, label);
    await signInThroughForwardAuth(popup, {
      origin: appHost(name),
      title: rungs.title,
      appShell: rungs.appShell,
    });
  });
}
