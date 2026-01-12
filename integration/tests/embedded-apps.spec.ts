import { test, expect, Page } from '../lib/fixtures';
import { Request, Response } from '@playwright/test';

interface ResourceError {
  url: string;
  status?: number;
  error?: string;
}

/**
 * Helper to track failed resource loads (404s, network errors)
 */
function trackResourceErrors(page: Page): ResourceError[] {
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

  return errors;
}

test.describe('Embedded Apps', () => {
  test.describe('Miniflux (supports BASE_URL)', () => {
    test('loads in iframe without resource errors', async ({ trackedPage: page, api }) => {
      const errors = trackResourceErrors(page);

      await api.ensureAppRunning('miniflux');
      await page.goto('/apps/miniflux/');

      // Iframe should be visible
      const iframe = page.locator('iframe');
      await expect(iframe).toBeVisible({ timeout: 15000 });

      // Check iframe source
      const src = await iframe.getAttribute('src');
      expect(src).toContain('/embed/miniflux');

      // Wait for iframe content
      const frame = page.frameLocator('iframe');
      await expect(frame.locator('body')).toBeVisible({ timeout: 30000 });

      // Verify miniflux loaded - should have login form or feed content
      await expect(frame.locator('form, .items, .page-header, a[href]').first()).toBeVisible({ timeout: 10000 });

      // Check for resource loading errors (CSS/JS 404s)
      const criticalErrors = errors.filter((e) => e.url.match(/\.(css|js)(\?|$)/));
      expect(criticalErrors, `CSS/JS resource loading errors: ${JSON.stringify(criticalErrors, null, 2)}`).toHaveLength(0);
    });
  });

  test.describe('Actual Budget (requires URL rewriting)', () => {
    test('loads in iframe without resource errors', async ({ trackedPage: page, api }) => {
      const resourceErrors = trackResourceErrors(page);

      await api.ensureAppRunning('actual-budget');
      await page.goto('/apps/actual-budget/');

      // Iframe should be visible
      const iframe = page.locator('iframe');
      await expect(iframe).toBeVisible({ timeout: 15000 });

      // Check iframe source
      const src = await iframe.getAttribute('src');
      expect(src).toContain('/embed/actual-budget');

      // Wait for iframe content
      const frame = page.frameLocator('iframe');
      await expect(frame.locator('body')).toBeVisible({ timeout: 30000 });

      // Wait for app to load
      await page.waitForTimeout(3000);

      // Check for SharedArrayBuffer error - requires COOP/COEP headers
      const hasSharedArrayBufferError = await frame.getByText('SharedArrayBuffer').isVisible().catch(() => false);
      if (hasSharedArrayBufferError) {
        throw new Error(
          'Actual Budget failed: SharedArrayBuffer not available.\n' +
            'Server needs Cross-Origin-Opener-Policy and Cross-Origin-Embedder-Policy headers.'
        );
      }

      // Wait for app to get past initialization
      await expect(async () => {
        const isInitializing = await frame.getByText('Initializing the connection').isVisible().catch(() => false);
        expect(isInitializing, 'App stuck on initialization screen').toBe(false);
      }).toPass({ timeout: 15000 });

      // Check for Fatal Error dialog - this is a critical failure
      const hasFatalError = await frame.getByText('Fatal Error').isVisible().catch(() => false);
      if (hasFatalError) {
        throw new Error(
          'Actual Budget crashed with Fatal Error dialog.\n' +
            'This is caused by React error #321 in the BackgroundImage component.\n' +
            'The app is not functional.'
        );
      }

      // Verify app reached a functional page (login, setup, or budget UI)
      const hasLogin = await frame.getByText('Sign in').first().isVisible().catch(() => false);
      const hasServerConfig = await frame.getByText('No server configured').isVisible().catch(() => false);
      const hasBudgetUI = await frame.getByRole('button').first().isVisible().catch(() => false);

      expect(
        hasLogin || hasServerConfig || hasBudgetUI,
        'App did not reach a functional page. Expected login, server config, or budget UI.'
      ).toBe(true);

      // If login page is showing, verify it can connect to the correct server
      if (hasLogin) {
        // The server URL should point to the actual-budget backend, not localhost:8080
        const serverText = (await frame.locator('text=Using server').textContent().catch(() => '')) ?? '';
        if (serverText.includes('localhost:8080')) {
          throw new Error(
            'Actual Budget is configured to connect to localhost:8080 (Traefik) instead of its backend.\n' +
              'The app cannot connect to the server properly.'
          );
        }
      }

      // Check for resource loading errors
      const criticalResourceErrors = resourceErrors.filter((e) => e.url.match(/\.(css|js)(\?|$)/));
      expect(
        criticalResourceErrors,
        `CSS/JS resource loading errors: ${JSON.stringify(criticalResourceErrors, null, 2)}`
      ).toHaveLength(0);
    });
  });

  test.describe('AdGuard Home (requires URL rewriting)', () => {
    test('loads in iframe without resource errors', async ({ trackedPage: page, api }) => {
      const errors = trackResourceErrors(page);

      await api.ensureAppRunning('adguard-home');
      await page.goto('/apps/adguard-home/');

      // Iframe should be visible
      const iframe = page.locator('iframe');
      await expect(iframe).toBeVisible({ timeout: 15000 });

      // Check iframe source
      const src = await iframe.getAttribute('src');
      expect(src).toContain('/embed/adguard-home');

      // Wait a moment for resources to attempt loading
      await page.waitForTimeout(3000);

      // Check for resource loading errors FIRST - this is the root cause when AdGuard fails
      const criticalErrors = errors.filter((e) => e.url.match(/\.(css|js)(\?|$)/));
      expect(criticalErrors, `CSS/JS resource loading errors (service worker URL rewriting not working):\n${JSON.stringify(criticalErrors, null, 2)}`).toHaveLength(0);

      // Wait for iframe content - will fail if CSS didn't load (body stays hidden)
      const frame = page.frameLocator('iframe');
      await expect(frame.locator('body')).toBeVisible({ timeout: 30000 });

      // Verify AdGuard loaded - should have setup wizard or dashboard
      await expect(frame.getByText(/AdGuard|Setup|Dashboard|DNS/i).first()).toBeVisible({ timeout: 10000 });
    });
  });
});

test.describe('Service Worker', () => {
  test('service worker is registered', async ({ trackedPage: page }) => {
    await page.goto('/');
    await page.waitForTimeout(2000);

    const swRegistered = await page.evaluate(async () => {
      if (!('serviceWorker' in navigator)) {
        return false;
      }
      const registration = await navigator.serviceWorker.getRegistration();
      return !!registration;
    });

    expect(swRegistered).toBe(true);
  });

  test('service worker is active', async ({ trackedPage: page }) => {
    await page.goto('/');
    await page.waitForTimeout(3000);

    const swActive = await page.evaluate(async () => {
      if (!('serviceWorker' in navigator)) {
        return false;
      }
      const registration = await navigator.serviceWorker.getRegistration();
      return registration?.active !== undefined;
    });

    expect(swActive).toBe(true);
  });
});
