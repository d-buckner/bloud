import type { Page, Request, Response, ConsoleMessage } from '@playwright/test';
import { writeFileSync, mkdirSync } from 'fs';
import { join } from 'path';

export interface NetworkEntry {
  timestamp: number;
  method: string;
  url: string;
  status?: number;
  duration?: number;
  requestHeaders?: Record<string, string>;
  responseHeaders?: Record<string, string>;
  requestBody?: string;
  responseBody?: string;
  error?: string;
}

export interface ConsoleEntry {
  timestamp: number;
  type: string;
  text: string;
  location?: string;
}

export interface DebugData {
  testName: string;
  startTime: number;
  endTime?: number;
  console: ConsoleEntry[];
  network: NetworkEntry[];
  errors: string[];
}

/**
 * Collects console logs and network requests during a test for debugging failures.
 * Automatically saves to a file when the test fails.
 */
export class DebugCollector {
  private data: DebugData;
  private pendingRequests = new Map<Request, { startTime: number }>();

  constructor(testName: string) {
    this.data = {
      testName,
      startTime: Date.now(),
      console: [],
      network: [],
      errors: [],
    };
  }

  /**
   * Attach to a page to collect console logs and network traffic.
   */
  attach(page: Page): void {
    // Collect console messages
    page.on('console', (msg: ConsoleMessage) => {
      this.data.console.push({
        timestamp: Date.now(),
        type: msg.type(),
        text: msg.text(),
        location: msg.location()?.url,
      });
    });

    // Collect Service Worker console messages
    page.context().on('serviceworker', (worker) => {
      worker.on('console', (msg: ConsoleMessage) => {
        this.data.console.push({
          timestamp: Date.now(),
          type: msg.type(),
          text: `[SW] ${msg.text()}`,
          location: msg.location()?.url,
        });
      });
    });

    // Collect page errors
    page.on('pageerror', (error: Error) => {
      this.data.errors.push(`[${new Date().toISOString()}] ${error.message}\n${error.stack}`);
    });

    // Track network requests
    page.on('request', (request: Request) => {
      this.pendingRequests.set(request, { startTime: Date.now() });
    });

    // Track network responses
    page.on('response', async (response: Response) => {
      const request = response.request();
      const pending = this.pendingRequests.get(request);
      const startTime = pending?.startTime || Date.now();

      const entry: NetworkEntry = {
        timestamp: startTime,
        method: request.method(),
        url: request.url(),
        status: response.status(),
        duration: Date.now() - startTime,
      };

      // Capture headers for API calls
      if (request.url().includes('/api/') || request.url().includes('/embed/')) {
        entry.requestHeaders = request.headers();
        entry.responseHeaders = response.headers();

        // Capture request body for POST/PUT
        if (['POST', 'PUT', 'PATCH'].includes(request.method())) {
          try {
            entry.requestBody = request.postData() || undefined;
          } catch {
            // Ignore if body not available
          }
        }

        // Capture response body for JSON responses
        const contentType = response.headers()['content-type'] || '';
        if (contentType.includes('application/json')) {
          try {
            const body = await response.text();
            entry.responseBody = body.slice(0, 10000); // Limit size
          } catch {
            // Ignore if body not available
          }
        }
      }

      this.data.network.push(entry);
      this.pendingRequests.delete(request);
    });

    // Track failed requests
    page.on('requestfailed', (request: Request) => {
      const pending = this.pendingRequests.get(request);
      const startTime = pending?.startTime || Date.now();

      this.data.network.push({
        timestamp: startTime,
        method: request.method(),
        url: request.url(),
        error: request.failure()?.errorText || 'Unknown error',
        duration: Date.now() - startTime,
      });

      this.pendingRequests.delete(request);
    });
  }

  /**
   * Add a custom error or note to the debug data.
   */
  addError(message: string): void {
    this.data.errors.push(`[${new Date().toISOString()}] ${message}`);
  }

  /**
   * Get all collected data.
   */
  getData(): DebugData {
    return {
      ...this.data,
      endTime: Date.now(),
    };
  }

  /**
   * Save collected data to a JSON file.
   */
  save(outputDir: string): string {
    this.data.endTime = Date.now();

    mkdirSync(outputDir, { recursive: true });

    const safeName = this.data.testName.replace(/[^a-zA-Z0-9-_]/g, '_');
    const filename = `${safeName}-${this.data.startTime}.json`;
    const filepath = join(outputDir, filename);

    writeFileSync(filepath, JSON.stringify(this.data, null, 2));

    return filepath;
  }

  /**
   * Print a summary of collected data to console.
   */
  printSummary(): void {
    console.log('\n=== Debug Collector Summary ===');
    console.log(`Test: ${this.data.testName}`);
    console.log(`Duration: ${(Date.now() - this.data.startTime) / 1000}s`);
    console.log(`Console messages: ${this.data.console.length}`);
    console.log(`Network requests: ${this.data.network.length}`);
    console.log(`Errors: ${this.data.errors.length}`);

    if (this.data.errors.length > 0) {
      console.log('\nErrors:');
      this.data.errors.forEach((e) => console.log(`  - ${e}`));
    }

    const failedRequests = this.data.network.filter((n) => n.error || (n.status && n.status >= 400));
    if (failedRequests.length > 0) {
      console.log('\nFailed requests:');
      failedRequests.forEach((r) => {
        console.log(`  - ${r.method} ${r.url} -> ${r.error || r.status}`);
      });
    }

    const consoleErrors = this.data.console.filter((c) => c.type === 'error');
    if (consoleErrors.length > 0) {
      console.log('\nConsole errors:');
      consoleErrors.forEach((c) => {
        console.log(`  - ${c.text}`);
      });
    }
  }
}
