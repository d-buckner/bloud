import { test as base, expect, type Page, type Request, type Response, type APIRequestContext } from '@playwright/test';
import { DebugCollector } from './debug-collector';
import { ApiClient } from './api-client';

interface ResourceError {
  url: string;
  status?: number;
  error?: string;
}

/**
 * Fixtures available in app tests.
 */
export interface AppTestFixtures {
  /** App name (auto-detected from test file path) */
  appName: string;
  /** App path: /apps/{appName}/ */
  appPath: string;
  /** Embed path: /embed/{appName}/ */
  embedPath: string;
  /** API client for backend interactions */
  api: ApiClient;
  /** Playwright request context for HTTP requests */
  request: APIRequestContext;
  /** Page with debug collector attached */
  page: Page;
  /** Navigate to the app and wait for iframe */
  openApp: () => Promise<ReturnType<Page['frameLocator']>>;
  /** Track resource loading errors (404s, network failures) */
  resourceErrors: ResourceError[];
}

/**
 * Create a test instance for an app.
 *
 * Auto-detects app name from the test file path:
 *   apps/miniflux/test.ts -> appName = "miniflux"
 *   apps/actual-budget/test.ts -> appName = "actual-budget"
 *
 * Usage:
 *   import { test, expect } from '../../integration/lib/app-test';
 *
 *   test('loads without errors', async ({ openApp, resourceErrors }) => {
 *     const frame = await openApp();
 *     await expect(frame.locator('body')).toBeVisible();
 *     expect(resourceErrors.filter(e => e.url.match(/\.(css|js)$/))).toHaveLength(0);
 *   });
 */
export const test = base.extend<AppTestFixtures>({
  // Auto-detect app name from test file path
  appName: async ({}, use, testInfo) => {
    const testFile = testInfo.file;
    // Extract app name from path: apps/<app-name>/test.ts
    const match = testFile.match(/apps\/([^/]+)\/test\.ts$/);
    if (!match) {
      throw new Error(
        `Could not detect app name from test file path: ${testFile}\n` +
        `Expected path like: apps/<app-name>/test.ts`
      );
    }
    await use(match[1]);
  },

  // App path: /apps/{appName}/
  appPath: async ({ appName }, use) => {
    await use(`/apps/${appName}/`);
  },

  // Embed path: /embed/{appName}/
  embedPath: async ({ appName }, use) => {
    await use(`/embed/${appName}/`);
  },

  // API client
  api: async ({ request }, use) => {
    const client = new ApiClient(request);
    await use(client);
  },

  // Page with debug collector
  page: async ({ page }, use, testInfo) => {
    const collector = new DebugCollector(testInfo.title);
    collector.attach(page);

    await use(page);

    // Save debug data on failure
    if (testInfo.status !== 'passed') {
      const outputDir = testInfo.outputDir;
      const filepath = collector.save(outputDir);
      console.log(`\nDebug data saved to: ${filepath}`);
      collector.printSummary();
    }
  },

  // Resource error tracking
  resourceErrors: async ({ page }, use) => {
    const errors: ResourceError[] = [];

    page.on('requestfailed', (request: Request) => {
      errors.push({
        url: request.url(),
        error: request.failure()?.errorText,
      });
    });

    page.on('response', (response: Response) => {
      if (response.status() >= 400) {
        errors.push({
          url: response.url(),
          status: response.status(),
        });
      }
    });

    await use(errors);
  },

  // Helper to open app and get iframe
  openApp: async ({ page, appName, appPath, embedPath, api }, use) => {
    const opener = async () => {
      await api.ensureAppRunning(appName);
      await page.goto(appPath);

      // Wait for iframe
      const iframe = page.locator('iframe');
      await expect(iframe).toBeVisible({ timeout: 15000 });

      // Verify iframe source
      const src = await iframe.getAttribute('src');
      expect(src).toContain(embedPath.slice(0, -1)); // remove trailing slash for contains check

      // Return frame locator for content assertions
      const frame = page.frameLocator('iframe');
      await expect(frame.locator('body')).toBeVisible({ timeout: 30000 });

      return frame;
    };

    await use(opener);
  },
});

export { expect };

/**
 * Filter resource errors to only CSS/JS failures (the critical ones).
 */
export function criticalErrors(errors: ResourceError[]): ResourceError[] {
  return errors.filter(e => e.url.match(/\.(css|js)(\?|$)/));
}
