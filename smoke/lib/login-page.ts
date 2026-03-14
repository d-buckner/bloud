import type { Page } from '@playwright/test';
import { TEST_CREDS } from '../tests/constants';

const SELECTORS = {
  USERNAME_FIELD: 'input[name="uidField"]',
  PASSWORD_FIELD: 'input[name="password"]',
} as const;

const LOGIN_PATH = '/if/flow';

export class LoginPage {
  constructor(private readonly page: Page) {}

  async login(): Promise<void> {
    await this.page.locator(SELECTORS.USERNAME_FIELD).waitFor({ state: 'visible', timeout: 90_000 });
    await this.page.locator(SELECTORS.USERNAME_FIELD).click();
    await this.page.locator(SELECTORS.USERNAME_FIELD).pressSequentially(TEST_CREDS.USERNAME);
    await this.page.getByRole('button', { name: 'Log in' }).click();

    await this.page.locator(SELECTORS.PASSWORD_FIELD).waitFor({ state: 'visible', timeout: 10_000 });
    await this.page.locator(SELECTORS.PASSWORD_FIELD).click();
    await this.page.waitForTimeout(1000);
    await this.page.locator(SELECTORS.PASSWORD_FIELD).pressSequentially(TEST_CREDS.PASSWORD);
    await this.page.getByRole('button', { name: 'Continue' }).click();
  }

  // Navigates to / and logs in only if redirected to the Authentik login page.
  async ensureSignedIn(): Promise<void> {
    await this.page.goto('/');
    // The SvelteKit app loads at / first, then JS redirects to /if/flow if the
    // session is invalid. Wait up to 5s for that redirect; if it doesn't happen
    // we're already authenticated.
    const isAuthPage = await this.page
      .waitForURL(url => url.href.includes(LOGIN_PATH), { timeout: 5_000 })
      .then(() => true)
      .catch(() => false);
    if (!isAuthPage) return;
    await this.login();
    await this.page.waitForURL('/', { timeout: 30_000 });
  }
}
