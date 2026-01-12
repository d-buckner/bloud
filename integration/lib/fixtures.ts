import { test as base, type Page } from '@playwright/test';
import { DebugCollector } from './debug-collector';
import { ApiClient } from './api-client';

/**
 * Extended test fixtures for Bloud integration tests.
 */
export interface BloudFixtures {
  /** Debug collector that captures console logs and network requests */
  debugCollector: DebugCollector;
  /** API client for direct API interactions */
  api: ApiClient;
  /** Page with debug collector attached */
  trackedPage: Page;
}

export const test = base.extend<BloudFixtures>({
  // Debug collector for capturing logs and network requests
  debugCollector: async ({ }, use, testInfo) => {
    const collector = new DebugCollector(testInfo.title);
    await use(collector);

    // Save debug data on test failure
    if (testInfo.status !== 'passed') {
      const outputDir = testInfo.outputDir;
      const filepath = collector.save(outputDir);
      console.log(`\nDebug data saved to: ${filepath}`);
      collector.printSummary();
    }
  },

  // API client for direct backend interactions
  api: async ({ request }, use) => {
    const client = new ApiClient(request);
    await use(client);
  },

  // Page with debug collector automatically attached
  trackedPage: async ({ page, debugCollector }, use) => {
    debugCollector.attach(page);
    await use(page);
  },
});

export { expect, type Page } from '@playwright/test';

/**
 * User-facing apps that should be tested.
 * These are apps that appear in the UI and can be launched.
 */
export const USER_APPS = ['miniflux', 'actual-budget', 'adguard-home'] as const;

/**
 * Apps that require URL rewriting via service worker.
 */
export const REWRITE_APPS = ['actual-budget', 'adguard-home'] as const;

/**
 * System apps that provide infrastructure.
 */
export const SYSTEM_APPS = ['postgres', 'traefik', 'authentik'] as const;

/**
 * App dependencies - used to ensure proper installation order.
 */
export const APP_DEPENDENCIES: Record<string, string[]> = {
  miniflux: ['postgres'],
  authentik: ['postgres'],
  'actual-budget': [],
  'adguard-home': [],
};
