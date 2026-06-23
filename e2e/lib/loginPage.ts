import type { Page } from '@playwright/test';
import { TEST_CREDS } from '../tests/constants';

/**
 * Page Object Model for the Authentik login flow.
 * Handles the identifier-first login (username → password).
 */
export class LoginPage {
  constructor(private page: Page) {}

  get usernameField() {
    return this.page.locator('input[name="uidField"]');
  }

  get passwordField() {
    return this.page.locator('input[name="password"]');
  }

  get loginButton() {
    return this.page.getByRole('button', { name: 'Log in' });
  }

  get continueButton() {
    return this.page.getByRole('button', { name: 'Continue' });
  }

  async isVisible(): Promise<boolean> {
    return this.usernameField
      .isVisible({ timeout: 5_000 })
      .catch(() => false);
  }

  async login(
    username = TEST_CREDS.USERNAME,
    password = TEST_CREDS.PASSWORD,
  ): Promise<void> {
    await this.usernameField.click();
    await this.usernameField.fill(username);
    await this.loginButton.click();

    await this.passwordField.waitFor({ state: 'visible', timeout: 10_000 });
    await this.passwordField.click();
    await this.passwordField.fill(password);
    await this.continueButton.click();
  }

  /** If the login page is showing, complete it. Otherwise no-op. */
  async loginIfNeeded(
    username = TEST_CREDS.USERNAME,
    password = TEST_CREDS.PASSWORD,
  ): Promise<void> {
    if (await this.isVisible()) {
      await this.login(username, password);
    }
  }
}
