// SPDX-License-Identifier: AGPL-3.0-only
import { expect, request, test } from '@playwright/test';
import type { BrowserContext, Page } from '@playwright/test';
import {
  appHost,
  appOrigin,
  assertForwardAuthGate,
  assertInstalledInCatalog,
  assertRunningTile,
  CREDENTIALS,
  ensureInstalled,
  ensureSignedIn,
  getAppSSOStrategy,
  LDAP_ATTEMPT_TIMEOUT_MS,
  LDAP_SETTLE_TIMEOUT_MS,
  openAppFromHome,
  signInThroughForwardAuth,
  waitForLdapBackedAnswer,
} from '@bloud/browser-harness';

/**
 * Convergence budget for an app that does not name its own: enough for a
 * one- or two-container install to pull its images and finish first-run
 * on a fresh VM. Penpot's seven containers and Paperless-ngx's four
 * sidecars each declare a larger number where they sit.
 */
const DEFAULT_CONVERGENCE_TIMEOUT_MS = 12 * 60_000;

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

/**
 * The app-specific values the framework's LDAP rung needs. The mechanism is
 * shared; only these values vary per app.
 */
export interface LdapBackedRungs {
  /** Method that drives the app's LDAP-backed credential check. */
  method: string;
  /** Path requested with the Bloud account's own credential. */
  path: string;
  /** Headers the protocol needs. A DAV listing needs `Depth: 0`. */
  headers?: Record<string, string>;
  /** The status that proves the outpost verified the credential. */
  expectedStatus: number;
  /** Optional substring the settled body must carry. */
  bodyContains?: string;
}

export interface DescribeAppOptions {
  /**
   * The display name the dashboard shows for this app, for example
   * "qBittorrent". The generic rungs look the app up by that label, because
   * that is what a person reads on the tile and on the catalog card.
   */
  label: string;
  /**
   * Catalog ids to install before this app, in order. Seerr needs a media
   * server to finish its own first-run onboarding, and a per-app CI leg
   * installs nothing else, so the prerequisite has to be declared rather
   * than assumed from the order the suite happens to run in.
   */
  prerequisites?: string[];
  /**
   * Wall-clock budget for the convergence rung, covering the prerequisites
   * and this app. The default fits a one- or two-container app on a fresh
   * VM; an app with more images to pull names a bigger number.
   */
  convergeTimeoutMs?: number;
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
  /**
   * Register the standard rung for an app that verifies a Bloud account against
   * the identity provider's LDAP outpost.
   *
   * The rung is an infrastructure rung, so it runs with the convergence rungs
   * and before the app's own tests. It waits for the app's LDAP path to settle
   * instead of sampling it once, which is the difference between a run that
   * waits out an outpost restart and a run that reads a warm-up stall as a
   * broken app.
   *
   * The rung also reads the app's declared strategy from the catalog and fails
   * when the app no longer declares `ldap`. That keeps the option from staying
   * switched on after a migration and quietly asserting nothing.
   */
  ldap?: LdapBackedRungs;
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
 *
 * An app that verifies a Bloud account over LDAP adds `ldap: { method, path,
 * expectedStatus }` and gets the one provider-readiness rung, so it waits for
 * the outpost instead of sampling it once and reading a warm-up stall as a
 * broken app.
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

    registerConvergenceRungs(name, options, app);

    // The LDAP rung sits with the infrastructure rungs and ahead of the app's
    // own tests: it proves the provider path answers before any spec asserts a
    // verdict about a credential through it.
    if (options.ldap) {
      registerLdapRungs(name, options.label, options.ldap);
    }

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
 *
 * The rung bodies are identical for every app; what varies is declared in
 * `DescribeAppOptions`. Two apps need it: Seerr has to install Jellyfin
 * first, and the multi-container installs need a longer budget than the
 * default. Both are values, so they stay in the spec as data.
 */
function registerConvergenceRungs(
  name: string,
  options: DescribeAppOptions,
  app: AppSuite,
): void {
  test('converges to running', async () => {
    // Infrastructure rung: fresh-VM image pull + first-run convergence.
    // When this fails, the UI rungs behind it are skipped, which
    // distinguishes a broken install from a misbehaving app.
    test.setTimeout(options.convergeTimeoutMs ?? DEFAULT_CONVERGENCE_TIMEOUT_MS);
    for (const prerequisite of options.prerequisites ?? []) {
      await ensureInstalled(prerequisite);
    }
    await ensureInstalled(name);
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await assertInstalledInCatalog(app.page, options.label);
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await assertRunningTile(app.page, options.label);
  });
}

/**
 * The one rung every LDAP-backed app asserts: a Bloud account, verified against
 * the identity provider's LDAP outpost, reaches its own tree. Registered by
 * `describeApp`, never copied into a spec.
 */
function registerLdapRungs(
  name: string,
  label: string,
  rungs: LdapBackedRungs,
): void {
  test('admits a Bloud account through the identity provider LDAP', async () => {
    test.setTimeout(LDAP_SETTLE_TIMEOUT_MS + 60_000);

    // The rung is only honest while the app still declares the strategy it
    // exercises. A stale option would otherwise pass by asserting nothing.
    const strategy = await getAppSSOStrategy(name);
    expect(
      strategy,
      `${name} asks for the LDAP rung but the catalog declares strategy "${strategy}"`,
    ).toBe('ldap');

    // Credentials go on the context so the Basic challenge is answered the way
    // a real client answers it, not pre-emptively on the first request.
    const ctx = await request.newContext({
      baseURL: appOrigin(name),
      httpCredentials: {
        username: CREDENTIALS.username,
        password: CREDENTIALS.password,
      },
    });
    try {
      const res = await waitForLdapBackedAnswer(
        `${label} ${rungs.method} ${rungs.path}`,
        () =>
          ctx.fetch(rungs.path, {
            method: rungs.method,
            headers: rungs.headers,
            timeout: LDAP_ATTEMPT_TIMEOUT_MS,
          }),
        LDAP_SETTLE_TIMEOUT_MS,
      );
      expect(res.status()).toBe(rungs.expectedStatus);
      if (rungs.bodyContains) {
        expect(await res.text()).toContain(rungs.bodyContains);
      }
    } finally {
      await ctx.dispose();
    }
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
    await assertForwardAuthGate(browser, name);
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
