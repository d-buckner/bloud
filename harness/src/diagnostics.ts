// SPDX-License-Identifier: AGPL-3.0-only
import type { Page } from '@playwright/test';
import http from 'node:http';
import https from 'node:https';

/**
 * One thing that went wrong while a page was loading.
 *
 * Every manual probe in this repo re-derived this capture from scratch and
 * usually got part of it wrong, which is the main reason a hand-written probe
 * disagreed with the suite. Collecting it once, here, is the point.
 */
export interface Problem {
  kind: 'requestfailed' | 'http' | 'console' | 'pageerror' | 'ws';
  detail: string;
  url?: string;
}

/**
 * Everything a page asked for, so a hang can be told apart from a slow load.
 *
 * A request that is still outstanding when the page goes quiet is the single
 * most useful signal for "the page never finished", and it is invisible to a
 * test that only asserts on one element.
 */
export interface PageActivity {
  pending: string[];
  finished: number;
  problems: Problem[];
}

/**
 * Records what a page did over its lifetime.
 *
 * Attach it before the first navigation: a listener added after `goto` misses
 * the document request and every subresource that raced it, which is exactly
 * where a broken proxy or a bad TLS handshake shows up.
 */
export class Diagnostics {
  private readonly problems: Problem[] = [];
  private readonly started = new Map<string, number>();
  private readonly settled = new Set<string>();
  private readonly sockets: { url: string; ok: boolean; detail: string }[] = [];

  constructor(private readonly page: Page) {
    page.on('request', (req) => {
      this.started.set(req.url(), Date.now());
    });
    page.on('requestfinished', (req) => {
      this.settled.add(req.url());
    });
    page.on('requestfailed', (req) => {
      this.settled.add(req.url());
      this.problems.push({
        kind: 'requestfailed',
        url: req.url(),
        detail: `${req.resourceType()} ${req.failure()?.errorText ?? 'failed'}`,
      });
    });
    page.on('response', (res) => {
      if (res.status() >= 400) {
        this.problems.push({
          kind: 'http',
          url: res.url(),
          detail: `${res.status()} on ${res.request().method()}`,
        });
      }
    });
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        this.problems.push({ kind: 'console', detail: msg.text() });
      }
    });
    page.on('pageerror', (err) => {
      this.problems.push({ kind: 'pageerror', detail: String(err).split('\n')[0] });
    });
    page.on('websocket', (ws) => {
      this.sockets.push({ url: ws.url(), ok: true, detail: 'opened' });
      this.problems.push({ kind: 'ws', url: ws.url(), detail: 'opened' });
      ws.on('socketerror', (text) => {
        // A failed upgrade is recorded against the socket, not only as a
        // generic problem: a reverse proxy that drops the Upgrade header
        // produces a page that loads, looks healthy, and then never renders,
        // because the app's only data channel died during the handshake.
        const rec = this.sockets.find((s) => s.url === ws.url());
        if (rec) {
          rec.ok = false;
          rec.detail = text;
        }
        this.problems.push({ kind: 'ws', url: ws.url(), detail: `error: ${text}` });
      });
    });
  }

  /**
   * WebSockets the page tried to open, and whether each one upgraded.
   *
   * This is the check that separates "the app is slow" from "a proxy in front
   * of the app cannot carry an upgrade". The HTTP status of a failed upgrade is
   * produced by the upstream app, not the proxy, so it reads as an app fault
   * unless you look at the socket.
   */
  get webSockets(): { url: string; ok: boolean; detail: string }[] {
    return [...this.sockets];
  }

  /** Requests with no finish and no failure: still in flight right now. */
  get pending(): string[] {
    return [...this.started.keys()].filter((u) => !this.settled.has(u));
  }

  get problems_(): Problem[] {
    return [...this.problems];
  }

  /** Everything worth printing after a failure, newest last. */
  report(limit = 25): string {
    const lines = this.problems.map(
      (p) => `${p.kind.toUpperCase().padEnd(13)} ${p.detail}  ${p.url ?? ''}`.trimEnd(),
    );
    for (const url of this.pending) {
      lines.push(`PENDING       ${url}`);
    }
    return lines.slice(0, limit).join('\n') || '(no problems recorded)';
  }

  /** Problems that mean the page is actually broken, not merely noisy. */
  failures(): Problem[] {
    return this.problems.filter(
      (p) =>
        p.kind !== 'ws' &&
        // A cancelled subresource is usually the browser abandoning a request
        // when the document navigated away, not a fault in the app.
        !(p.kind === 'requestfailed' && /ERR_ABORTED/.test(p.detail)),
    );
  }
}

/**
 * Try a WebSocket upgrade against a URL and report what came back.
 *
 * Deliberately not a browser: an unauthenticated Home Assistant page renders a
 * sign-in screen and opens no socket at all, so a browser-based probe cannot
 * tell "the proxy dropped the upgrade" from "nobody tried to connect". A raw
 * handshake needs no credentials and answers the proxy question on its own.
 *
 * A reverse proxy that drops the hop-by-hop `Upgrade`/`Connection` headers
 * produces an upstream that answers 400 with "No WebSocket UPGRADE hdr", while
 * every page on the same origin loads normally.
 */
export function probeWebSocketUpgrade(
  url: string,
  timeoutMs = 15_000,
): Promise<{ ok: boolean; status?: number; detail: string }> {
  return new Promise((resolve) => {
    const target = new URL(url);
    const lib = target.protocol === 'https:' ? https : http;
    const req = lib.request(
      {
        hostname: target.hostname,
        port: target.port || (target.protocol === 'https:' ? 443 : 80),
        path: target.pathname + target.search,
        method: 'GET',
        rejectUnauthorized: false,
        timeout: timeoutMs,
        headers: {
          Connection: 'Upgrade',
          Upgrade: 'websocket',
          'Sec-WebSocket-Version': '13',
          'Sec-WebSocket-Key': 'x3JJHMbDL1EzLkh9GBhXDw==',
          Host: target.host,
          Origin: `${target.protocol}//${target.host}`,
        },
      },
      (res) => {
        const status = res.statusCode ?? 0;
        res.resume();
        resolve({
          ok: status === 101,
          status,
          detail: status === 101 ? 'switching protocols' : `HTTP ${status}`,
        });
      },
    );
    req.on('upgrade', (_res, socket) => {
      socket.destroy();
      resolve({ ok: true, detail: 'switching protocols' });
    });
    req.on('timeout', () => {
      req.destroy();
      resolve({ ok: false, detail: `timed out after ${timeoutMs}ms` });
    });
    req.on('error', (err) => resolve({ ok: false, detail: String(err.message) }));
    req.end();
  });
}

/**
 * Visible text of a page, piercing open shadow roots.
 *
 * `document.body.innerText` stops at a shadow boundary, so a page built from
 * web components (Authentik's flow interface, Home Assistant's whole shell)
 * reports empty text while rendering perfectly. Reading it as empty and
 * concluding "the page did not load" is the specific mistake this prevents.
 */
export async function visibleText(page: Page): Promise<string> {
  const read = () =>
    page.evaluate(() => {
      const out: string[] = [];
      const walk = (root: Document | ShadowRoot | Element): void => {
        for (const node of Array.from(root.children) as HTMLElement[]) {
          if (node.shadowRoot) {
            walk(node.shadowRoot);
          }
          const style = getComputedStyle(node);
          if (style.display === 'none' || style.visibility === 'hidden') {
            continue;
          }
          const text = (node.innerText ?? '').trim();
          if (text) {
            out.push(text);
          }
        }
      };
      if (document.body) {
        walk(document.body);
      }
      // Siblings repeat their ancestors' text; dedupe keeps the output readable.
      return [...new Set(out)].join('\n');
    });

  // A page that is still redirecting destroys the execution context under the
  // read. Home Assistant always bounces an anonymous visitor through
  // /auth/oidc/welcome, so a probe that reads too eagerly is not broken, it is
  // just early. Retry until the document stops moving.
  const deadline = Date.now() + 15_000;
  for (;;) {
    try {
      return await read();
    } catch (err) {
      if (!/Execution context was destroyed|navigation/.test(String(err)) || Date.now() > deadline) {
        throw err;
      }
      await page.waitForLoadState('domcontentloaded').catch(() => {});
    }
  }
}
