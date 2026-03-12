import type { Page } from '@playwright/test';

export class LoginPage {
  constructor(private readonly page: Page) {}

  async login(username: string, password: string): Promise<void> {
    await this.page.locator('input[name="uidField"]').waitFor({ state: 'visible', timeout: 30_000 });
    await this.page.locator('input[name="uidField"]').click();
    await this.page.locator('input[name="uidField"]').pressSequentially(username);
    await this.page.getByRole('button', { name: 'Log in' }).click();

    await this.page.locator('input[name="password"]').waitFor({ state: 'visible', timeout: 10_000 });
    await this.page.locator('input[name="password"]').click();
    await this.page.locator('input[name="password"]').pressSequentially(password);
    await this.page.getByRole('button', { name: 'Continue' }).click();
  }
}
