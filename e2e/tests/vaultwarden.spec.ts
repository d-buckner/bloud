// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect } from '../lib/fixtures';
import type { Page } from '@playwright/test';
import { describeApp } from '../lib/app-suite';
import {
  expectInstalledInCatalog,
  expectRunningTile,
  openAppFromHome,
} from '../lib/apps';
import { ensureInstalled } from '../lib/api';
import { LoginPage } from '../lib/loginPage';
import { TEST_CREDS } from './constants';

const VAULTWARDEN_URL = 'http://vaultwarden.localhost:8080';

// The Bitwarden web client refuses to talk to any server whose URL is not
// https:// (its isDev() build flag is off), and Bloud serves apps over plain
// HTTP today, so past the sign-in page the web vault only works when the
// opt-in dev switch is on. The switch is an environment variable on the
// host-agent (see apps/vaultwarden/INTEGRATION.md, "Plain HTTP"); this suite
// reads the same variable to know whether the rungs that need it can run.
const PLAIN_HTTP_SWITCH = ['1', 'true', 'yes'].includes(
  (process.env.BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP ?? '').trim().toLowerCase(),
);

const MASTER_PASSWORD = 'Bloud-e2e-master-password-1';

// One test case per observable behavior; serial mode (from describeApp)
// means the first failure skips the rungs behind it. Vaultwarden uses
// native-oidc with SSO_ONLY: the web vault's login page offers only "Use single
// sign-on", the identity provider login happens on the issuer origin (where
// this context has no session), and a first sign-in asks for a master password
// because SSO never replaces it (the vault is encrypted under it).
describeApp('vaultwarden', (app) => {
  test('converges to running', async () => {
    // Infrastructure rung: a single container on its own SQLite database. When
    // this fails, the UI rungs below are skipped, which distinguishes a broken
    // install from a misbehaving app.
    test.setTimeout(10 * 60_000);
    await ensureInstalled('vaultwarden');
  });

  test('appears in the catalog as installed', async () => {
    test.setTimeout(60_000);
    await expectInstalledInCatalog(app.page, 'Vaultwarden');
  });

  test('appears on the home screen as a converged tile', async () => {
    test.setTimeout(60_000);
    await app.page.goto('/');
    await expectRunningTile(app.page, 'Vaultwarden');
  });

  test('the sign-in page offers single sign-on only', async () => {
    test.setTimeout(120_000);
    const vault = await openAppFromHome(app.page, 'Vaultwarden');
    try {
      await expect(vault).toHaveURL(/#\/login/, { timeout: 60_000 });
      await expect(vault.getByRole('button', { name: /single sign-on/i })).toBeVisible();

      // SSO_ONLY: the identity provider is the only way in. Master-password
      // login and self-registration are gone from the page.
      await expect(vault.getByRole('button', { name: 'Other' })).toHaveCount(0);
      await expect(vault.getByRole('link', { name: 'Create account' })).toHaveCount(0);
    } finally {
      await vault.close();
    }
  });

  test('signs in through OIDC and reaches the vault', async () => {
    test.skip(
      !PLAIN_HTTP_SWITCH,
      'the web vault refuses plain-HTTP servers; set BLOUD_DEV_VAULTWARDEN_ALLOW_HTTP=1 on the host-agent and here ' +
        '(apps/vaultwarden/INTEGRATION.md, "Plain HTTP")',
    );
    test.setTimeout(360_000);
    const vault = await app.page.context().newPage();
    try {
      await vault.goto(VAULTWARDEN_URL);

      await startSsoLogin(vault);

      // Complete the identity provider login on the issuer origin. The flow
      // hops through /if/flow/... steps that re-render after document load, so
      // poll within a deadline instead of checking once. A first sign-in lands
      // on the master password page; a returning one on the lock screen.
      const loginPage = new LoginPage(vault);
      const deadline = Date.now() + 240_000;
      for (;;) {
        if (/#\/(set-initial-password|lock|setup-extension|vault)/.test(vault.url())) break;
        if (Date.now() > deadline) break;

        if (await loginPage.isVisible()) {
          await loginPage.login();
          continue;
        }

        await vault.waitForTimeout(500);
      }

      // SSO created the account and handed the browser back to the app. Because
      // SSO never replaces the master password, a first sign-in asks the user to
      // set one; the vault only opens once it is set.
      if (/#\/set-initial-password/.test(vault.url())) {
        const password = vault.locator('input[type="password"]:visible');
        await expect(password).toHaveCount(2, { timeout: 30_000 });
        await password.nth(0).fill(MASTER_PASSWORD);
        await password.nth(1).fill(MASTER_PASSWORD);
        await vault.getByRole('button', { name: 'Create account' }).click();
      }

      // A returning account (a persistent dev instance, or a re-run) is signed in
      // but locked: the vault opens only with the master password.
      if (/#\/lock/.test(vault.url())) {
        await vault.locator('input[type="password"]:visible').fill(MASTER_PASSWORD);
        await vault.getByRole('button', { name: /unlock/i }).click();
      }

      // Past the master password the client generates the account keys and moves
      // on to its onboarding page or the vault itself. Either proves the client
      // accepted the server and could do its cryptography.
      await expect(vault).toHaveURL(/#\/(setup-extension|vault)/, { timeout: 120_000 });
      await expect(vault.locator('input[type="email"]:visible')).toHaveCount(0);
    } finally {
      await vault.close();
    }
  });
});

/**
 * Start the SSO login from the web vault's sign-in page: type an email, click
 * "Use single sign-on". With the client able to reach the server, Vaultwarden's
 * emulated domain lookup lets the client skip its SSO identifier prompt and go
 * straight to the identity provider, so there is nothing else to type. The
 * email is only used to route to SSO: the account's email comes from the
 * identity provider.
 */
async function startSsoLogin(vault: Page): Promise<void> {
  const email = vault.locator('input[type="email"]:visible');
  await email.waitFor({ state: 'visible', timeout: 60_000 });
  await email.fill(`${TEST_CREDS.USERNAME}@bloud.test`);
  await vault.getByRole('button', { name: /single sign-on/i }).click();
}
