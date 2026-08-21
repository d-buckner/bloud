// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { test as base, expect, type Page } from '@playwright/test';
import { ensureSignedIn } from './auth';
import * as api from './api';

interface BloudFixtures {
  /** A page that has already completed Authentik SSO login. */
  authenticatedPage: Page;
  /** Host-agent API helpers (loopback, no auth needed). */
  api: typeof api;
}

export const test = base.extend<BloudFixtures>({
  authenticatedPage: async ({ page }, use) => {
    await ensureSignedIn(page);
    await use(page);
  },
  api: async ({}, use) => {
    await use(api);
  },
});

export { expect };
