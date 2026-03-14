import type { Page } from '@playwright/test';

const SELECTORS = {
  USERNAME_FIELD: 'input[name="uidField"]',
  PASSWORD_FIELD: 'input[name="password"]',
} as const;

export class LoginPage {
  constructor(private readonly page: Page) {}

  async login(username: string, password: string): Promise<void> {
    await this.page.locator(SELECTORS.USERNAME_FIELD).waitFor({ state: 'visible', timeout: 90_000 });
    await this.page.locator(SELECTORS.USERNAME_FIELD).click();
    await this.page.locator(SELECTORS.USERNAME_FIELD).pressSequentially(username);
    await this.page.getByRole('button', { name: 'Log in' }).click();

    await this.page.locator(SELECTORS.PASSWORD_FIELD).waitFor({ state: 'visible', timeout: 10_000 });
    await this.page.locator(SELECTORS.PASSWORD_FIELD).click();
    await this.page.waitForTimeout(1000);
    await this.page.locator(SELECTORS.PASSWORD_FIELD).pressSequentially(password);
    await this.page.getByRole('button', { name: 'Continue' }).click();
  }
}
