/**
 * Debug script for AdGuard Home setup wizard network errors.
 *
 * Run with:
 *   cd integration && npx tsx scripts/debug-adguard-setup.ts
 *
 * Or with headed browser:
 *   cd integration && npx tsx scripts/debug-adguard-setup.ts --headed
 */

import { chromium, Browser, Page, Request, Response } from 'playwright';

const BASE_URL = process.env.BASE_URL || 'http://localhost:8080';
const HEADED = process.argv.includes('--headed');

interface NetworkLog {
  timestamp: string;
  type: 'request' | 'response' | 'failed';
  method: string;
  url: string;
  status?: number;
  statusText?: string;
  headers?: Record<string, string>;
  postData?: string;
  responseBody?: string;
  error?: string;
  duration?: number;
}

const networkLogs: NetworkLog[] = [];
const requestTimes = new Map<Request, number>();

function log(message: string, data?: unknown) {
  const ts = new Date().toISOString();
  if (data) {
    console.log(`[${ts}] ${message}`, JSON.stringify(data, null, 2));
  } else {
    console.log(`[${ts}] ${message}`);
  }
}

function setupNetworkTracking(page: Page) {
  // Track all requests
  page.on('request', (request: Request) => {
    requestTimes.set(request, Date.now());
    const entry: NetworkLog = {
      timestamp: new Date().toISOString(),
      type: 'request',
      method: request.method(),
      url: request.url(),
      headers: request.headers(),
    };
    if (request.postData()) {
      entry.postData = request.postData() || undefined;
    }
    networkLogs.push(entry);
    log(`→ ${request.method()} ${request.url()}`);
  });

  // Track all responses
  page.on('response', async (response: Response) => {
    const request = response.request();
    const startTime = requestTimes.get(request);
    const duration = startTime ? Date.now() - startTime : undefined;

    const entry: NetworkLog = {
      timestamp: new Date().toISOString(),
      type: 'response',
      method: request.method(),
      url: response.url(),
      status: response.status(),
      statusText: response.statusText(),
      headers: response.headers(),
      duration,
    };

    // Capture response body for API calls and errors
    const contentType = response.headers()['content-type'] || '';
    if (
      response.status() >= 400 ||
      contentType.includes('json') ||
      response.url().includes('/api/') ||
      response.url().includes('/control/')
    ) {
      try {
        const body = await response.text();
        entry.responseBody = body.slice(0, 5000);
      } catch {
        // Body may not be available
      }
    }

    networkLogs.push(entry);

    const icon = response.status() >= 400 ? '✗' : '✓';
    log(`${icon} ${response.status()} ${request.method()} ${response.url()} (${duration}ms)`);

    if (response.status() >= 400 && entry.responseBody) {
      log('  Response body:', entry.responseBody);
    }
  });

  // Track failed requests
  page.on('requestfailed', (request: Request) => {
    const startTime = requestTimes.get(request);
    const duration = startTime ? Date.now() - startTime : undefined;

    const entry: NetworkLog = {
      timestamp: new Date().toISOString(),
      type: 'failed',
      method: request.method(),
      url: request.url(),
      error: request.failure()?.errorText || 'Unknown error',
      duration,
    };
    networkLogs.push(entry);
    log(`✗ FAILED ${request.method()} ${request.url()} - ${entry.error}`);
  });
}

function setupConsoleTracking(page: Page) {
  page.on('console', (msg) => {
    const type = msg.type();
    const text = msg.text();
    if (type === 'error') {
      log(`[CONSOLE ERROR] ${text}`);
    } else if (type === 'warning') {
      log(`[CONSOLE WARN] ${text}`);
    } else if (text.includes('[embed-sw]') || text.includes('adguard')) {
      log(`[CONSOLE] ${text}`);
    }
  });

  page.on('pageerror', (error) => {
    log(`[PAGE ERROR] ${error.message}`);
  });
}

async function setupServiceWorkerTracking(page: Page) {
  const context = page.context();

  context.on('serviceworker', (worker) => {
    log(`[SW] Service worker registered: ${worker.url()}`);

    worker.on('console', (msg) => {
      log(`[SW CONSOLE] ${msg.text()}`);
    });
  });
}

async function checkAdGuardStatus(page: Page) {
  log('Checking AdGuard Home status...');

  const response = await page.request.get(`${BASE_URL}/api/apps/installed`);
  const data = await response.json();
  // API returns array directly
  const apps = Array.isArray(data) ? data : (data.apps || []);
  const adguard = apps.find((a: { name: string }) => a.name === 'adguard-home');

  if (!adguard) {
    log('WARNING: AdGuard Home is not installed. Please install it first.');
    log('Continuing anyway to debug...');
  } else {
    log(`AdGuard Home status: ${adguard.status}`);
  }
}

async function waitForServiceWorker(page: Page) {
  log('Waiting for service worker to be active...');

  // First visit the main page to register SW
  await page.goto(BASE_URL);
  await page.waitForTimeout(2000);

  const swActive = await page.evaluate(async () => {
    if (!('serviceWorker' in navigator)) {
      return { registered: false, reason: 'serviceWorker not supported' };
    }

    const registration = await navigator.serviceWorker.getRegistration();
    if (!registration) {
      return { registered: false, reason: 'no registration' };
    }

    if (registration.active) {
      return { registered: true, active: true, scope: registration.scope };
    }

    // Wait for activation
    await new Promise<void>((resolve) => {
      if (registration.active) {
        resolve();
        return;
      }
      registration.addEventListener('updatefound', () => {
        const newWorker = registration.installing;
        if (newWorker) {
          newWorker.addEventListener('statechange', () => {
            if (newWorker.state === 'activated') {
              resolve();
            }
          });
        }
      });
    });

    return { registered: true, active: true, scope: registration.scope };
  });

  log('Service worker status:', swActive);
  return swActive;
}

async function navigateToAdGuard(page: Page) {
  log('Navigating to AdGuard Home embedded app...');

  await page.goto(`${BASE_URL}/apps/adguard-home/`);
  await page.waitForTimeout(2000);

  // Wait for iframe
  const iframe = page.locator('iframe');
  await iframe.waitFor({ state: 'visible', timeout: 15000 });

  const src = await iframe.getAttribute('src');
  log(`Iframe src: ${src}`);

  // Get the frame content
  const frame = page.frameLocator('iframe');

  // Wait for body to be visible
  try {
    await frame.locator('body').waitFor({ state: 'visible', timeout: 30000 });
    log('Iframe body is visible');
  } catch (e) {
    log('Iframe body not visible within timeout');
  }

  return frame;
}

async function interactWithSetupWizard(page: Page) {
  const frame = page.frameLocator('iframe');

  log('Waiting for setup wizard to render...');

  // Wait for React app to render - look for the root element to have content
  await page.waitForTimeout(3000);

  // Take screenshot for debugging
  const fs = await import('fs');
  fs.mkdirSync('test-results', { recursive: true });
  await page.screenshot({ path: 'test-results/adguard-setup-wizard.png', fullPage: true });
  log('Screenshot saved to test-results/adguard-setup-wizard.png');

  // List all buttons in the iframe
  const buttons = await frame.locator('button').all();
  log(`Found ${buttons.length} buttons in iframe`);
  for (const button of buttons) {
    const text = await button.textContent().catch(() => '(no text)');
    const isVisible = await button.isVisible().catch(() => false);
    log(`  Button: "${text?.trim()}" visible=${isVisible}`);
  }

  // List all links
  const links = await frame.locator('a').all();
  log(`Found ${links.length} links in iframe`);
  for (const link of links) {
    const text = await link.textContent().catch(() => '(no text)');
    const href = await link.getAttribute('href').catch(() => '(no href)');
    log(`  Link: "${text?.trim()}" href=${href}`);
  }

  // Check what's in the iframe body
  const bodyHtml = await frame.locator('body').innerHTML().catch(() => 'Unable to get body HTML');
  log('Current body content preview:', bodyHtml.slice(0, 2000));

  // Try to find and click "Get Started" button (AdGuard Home specific)
  log('Looking for Get Started button...');
  const getStartedBtn = frame.locator('button').filter({ hasText: /get started/i }).first();
  if (await getStartedBtn.isVisible().catch(() => false)) {
    log('Found "Get Started" button, clicking...');
    await getStartedBtn.click();
    await page.waitForTimeout(2000);
    log('Clicked Get Started, checking for network errors...');
  } else {
    log('Get Started button not found');
  }

  // Look for Next button after Get Started
  const nextBtn = frame.locator('button').filter({ hasText: /next/i }).first();
  if (await nextBtn.isVisible().catch(() => false)) {
    log('Found "Next" button, clicking...');
    await nextBtn.click();
    await page.waitForTimeout(3000);
    log('Clicked Next, checking for network errors...');

    // Log network errors after Next click
    const recentErrors = networkLogs
      .filter((l) => l.type === 'failed' || (l.type === 'response' && (l.status || 0) >= 400))
      .slice(-5);
    if (recentErrors.length > 0) {
      log('Recent network errors after Next click:', recentErrors);
    }
  } else {
    log('Next button not found');
  }

  // Try clicking Next again (step 2)
  await page.waitForTimeout(1000);
  const nextBtn2 = frame.locator('button').filter({ hasText: /next/i }).first();
  if (await nextBtn2.isVisible().catch(() => false)) {
    log('Found another "Next" button, clicking...');
    await nextBtn2.click();
    await page.waitForTimeout(3000);

    const recentErrors = networkLogs
      .filter((l) => l.type === 'failed' || (l.type === 'response' && (l.status || 0) >= 400))
      .slice(-5);
    if (recentErrors.length > 0) {
      log('Recent network errors after second Next click:', recentErrors);
    }
  }

  // Take another screenshot after interactions
  await page.screenshot({ path: 'test-results/adguard-setup-wizard-after.png', fullPage: true });
  log('Screenshot saved to test-results/adguard-setup-wizard-after.png');
}

function printSummary() {
  console.log('\n' + '='.repeat(80));
  console.log('NETWORK SUMMARY');
  console.log('='.repeat(80));

  const failures = networkLogs.filter((l) => l.type === 'failed');
  const errors = networkLogs.filter((l) => l.type === 'response' && (l.status || 0) >= 400);

  if (failures.length > 0) {
    console.log('\n❌ FAILED REQUESTS:');
    failures.forEach((f) => {
      console.log(`  ${f.method} ${f.url}`);
      console.log(`    Error: ${f.error}`);
    });
  }

  if (errors.length > 0) {
    console.log('\n⚠️  ERROR RESPONSES:');
    errors.forEach((e) => {
      console.log(`  ${e.status} ${e.method} ${e.url}`);
      if (e.responseBody) {
        console.log(`    Body: ${e.responseBody.slice(0, 200)}`);
      }
    });
  }

  // Group by endpoint
  const byEndpoint = new Map<string, NetworkLog[]>();
  networkLogs.forEach((l) => {
    const url = new URL(l.url);
    const key = `${l.method} ${url.pathname}`;
    if (!byEndpoint.has(key)) {
      byEndpoint.set(key, []);
    }
    byEndpoint.get(key)!.push(l);
  });

  console.log('\n📊 REQUESTS BY ENDPOINT:');
  byEndpoint.forEach((logs, endpoint) => {
    const requests = logs.filter((l) => l.type === 'request').length;
    const responses = logs.filter((l) => l.type === 'response');
    const failed = logs.filter((l) => l.type === 'failed' || (l.type === 'response' && (l.status || 0) >= 400));
    const icon = failed.length > 0 ? '❌' : '✓';
    console.log(`  ${icon} ${endpoint}: ${requests} req, ${responses.length} resp, ${failed.length} errors`);
  });

  if (failures.length === 0 && errors.length === 0) {
    console.log('\n✓ No network errors detected');
  }
}

async function main() {
  log(`Starting AdGuard setup wizard debug session (headed: ${HEADED})`);
  log(`Base URL: ${BASE_URL}`);

  let browser: Browser | null = null;

  try {
    browser = await chromium.launch({
      headless: !HEADED,
      slowMo: HEADED ? 100 : 0,
    });

    const context = await browser.newContext({
      viewport: { width: 1280, height: 720 },
    });

    const page = await context.newPage();

    // Set up all tracking
    setupNetworkTracking(page);
    setupConsoleTracking(page);
    await setupServiceWorkerTracking(page);

    // Check AdGuard status (no install/uninstall)
    await checkAdGuardStatus(page);

    // Wait for service worker
    await waitForServiceWorker(page);

    // Navigate to AdGuard
    await navigateToAdGuard(page);

    // Wait a bit for initial network activity
    await page.waitForTimeout(3000);

    // Interact with setup wizard
    await interactWithSetupWizard(page);

    // Final wait for any remaining network activity
    await page.waitForTimeout(2000);

    // Print summary
    printSummary();

    // Save full logs
    const fs = await import('fs');
    const logsPath = 'test-results/adguard-debug-network.json';
    fs.mkdirSync('test-results', { recursive: true });
    fs.writeFileSync(logsPath, JSON.stringify(networkLogs, null, 2));
    log(`Full network logs saved to: ${logsPath}`);

    if (HEADED) {
      log('Browser is open for manual inspection. Press Ctrl+C to close.');
      await new Promise(() => {}); // Keep open indefinitely
    }
  } catch (error) {
    log('Error:', error);
    printSummary();
    throw error;
  } finally {
    if (browser && !HEADED) {
      await browser.close();
    }
  }
}

main().catch((e) => {
  console.error(e);
  process.exit(1);
});
