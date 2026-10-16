// SPDX-License-Identifier: AGPL-3.0-only
import type { Locator } from '@playwright/test';
import { PageObject } from './base';
import {
  assertInstalledInCatalog,
  assertRunningTile,
  catalogCard,
  homeTile,
} from '../dashboard';
import { ensureSignedIn } from '../auth';

/**
 * The Bloud dashboard itself: sign-in, catalog grid, home grid.
 *
 * The locators live in `../dashboard` rather than being restated here, because
 * that module already carries the hard-won scoping rules (a display name is
 * not a unique key, and a card's text includes its description). This class is
 * the object-shaped surface over them, not a second copy.
 */
export class BloudPage extends PageObject {
  /** Complete Authentik SSO on the Bloud origin. No-ops when already signed in. */
  async signIn(): Promise<void> {
    await ensureSignedIn(this.page);
  }

  async openCatalog(): Promise<void> {
    await this.page.goto('/catalog');
  }

  async openHome(): Promise<void> {
    await this.page.goto('/');
  }

  card(label: string): Locator {
    return catalogCard(this.page, label);
  }

  tile(label: string): Locator {
    return homeTile(this.page, label);
  }

  async assertInstalled(label: string): Promise<void> {
    await this.openCatalog();
    await assertInstalledInCatalog(this.page, label);
  }

  async assertRunningTile(label: string): Promise<void> {
    await this.openHome();
    await assertRunningTile(this.page, label);
  }

  /** Labels currently drawn on the home grid, in DOM order. */
  async tileLabels(): Promise<string[]> {
    return (await this.page.locator('.app-label').allInnerTexts())
      .map((t) => t.trim())
      .filter(Boolean);
  }

  /** The per-app waiting page Traefik serves while an app is down. */
  async openLoadingPage(app: string): Promise<void> {
    await this.page.goto(`/bloud-loading/${app}`);
    await this.page.locator('body').waitFor({ state: 'visible' });
  }
}
