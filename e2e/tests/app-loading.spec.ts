// SPDX-License-Identifier: AGPL-3.0-only
import { test, expect, type Page } from '@playwright/test';
import { createServer, type Server } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

/**
 * The per-app waiting page, driven in a real browser against a fake app.
 *
 * This spec needs no Bloud runtime. It serves the committed page itself from
 * two local origins, so the redirect chain the page has to survive is a real
 * cross-origin redirect with real fetch semantics, and what is under test is
 * the artifact rather than a copy of it.
 *
 * The case that motivated it (issue #297): an SSO-backed app comes back by
 * redirecting, and the second hop leaves the origin for the identity provider.
 * A `fetch` in the default cors mode follows that chain, the browser refuses
 * the cross-origin response, and the poll's `catch` reads the one signal that
 * the app is back as a reason to keep waiting. The tab then sits on
 * "re-loading" over an app that has been up for hours.
 */

const LOADING_PATH = '/bloud-loading/hermes';
/** A deep link, so a reload that lands on the root instead shows up. */
const DEEP_LINK = `${LOADING_PATH}?next=%2Flibrary`;

/** How many polls the fake app is served for before it comes back. */
const POLLS_BEFORE_APP_ANSWERS = 1;

const TEMPLATE_PATH = resolve(
  dirname(fileURLToPath(import.meta.url)),
  '../../services/host-agent/internal/api/app_loading.html',
);

/**
 * The committed waiting page with the Go template filled in.
 *
 * The template is read from source rather than copied into this file, because a
 * copy keeps passing after the page it stands for regresses. The last assertion
 * is the drift guard: if the template grows a fourth variable, substituting
 * only these three leaves a literal `{{.Something}}` in the page the browser
 * runs, and this file would be testing a page that does not exist.
 */
async function waitingPage(): Promise<string> {
  const template = await readFile(TEMPLATE_PATH, 'utf8');
  const rendered = template
    .replace(/\{\{\.DisplayName\}\}/g, 'Hermes')
    .replace(/\{\{\.Initial\}\}/g, 'H')
    .replace(/\{\{\.IconDataURI\}\}/g, '');
  expect(rendered).not.toContain('{{');
  return rendered;
}

/** What the fake app does once it has come back. */
type AppAnswer = 'never' | 'cross-origin-redirect' | 'same-origin-redirect' | 'serving';

interface Deployment {
  appOrigin: string;
  issuerOrigin: string;
  /** The URL the page is waiting on, deep link included. */
  waitedURL: string;
  close(): Promise<void>;
}

function listen(server: Server): Promise<number> {
  return new Promise((fulfill, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (address === null || typeof address === 'string') {
        reject(new Error('the fake server bound no port'));
        return;
      }
      fulfill(address.port);
    });
  });
}

async function close(server: Server): Promise<void> {
  // The page polls over a keep-alive connection, so close() alone would wait on
  // an idle socket that only the browser decides to hang up on.
  server.closeAllConnections();
  await new Promise<void>((fulfill) => server.close(() => fulfill()));
}

/**
 * Two origins: an app that serves the waiting page until it has been polled
 * `POLLS_BEFORE_APP_ANSWERS` times and then answers `answer` for good, and an
 * identity provider that only a top-level navigation may reach.
 */
async function deploy(page: string, answer: AppAnswer): Promise<Deployment> {
  let polls = 0;

  const issuer = createServer((_req, res) => {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
    res.end('<!doctype html><html><body><p id="provider">Sign in to Authentik</p></body></html>');
  });
  const issuerOrigin = `http://127.0.0.1:${await listen(issuer)}`;

  const app = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://app.invalid');
    // The page's own poll is a fetch; the reload that ends the wait is a
    // navigation. Only the polls are counted, so the app comes back on a poll
    // rather than on whichever request happens to arrive next.
    if (req.headers['sec-fetch-mode'] === 'cors' && url.pathname === LOADING_PATH) polls++;

    if (answer === 'never' || polls <= POLLS_BEFORE_APP_ANSWERS) {
      // What Traefik hands back while the container is down: the waiting page,
      // marked, over an error status. The first request of all is the
      // navigation that got the page, before any poll, so it lands here too.
      // 502 because that is the only status left that can produce this: the
      // middleware used to cover 503 too, which is an app answering about
      // itself and must never be stood in for.
      res.writeHead(502, {
        'Content-Type': 'text/html; charset=utf-8',
        'Cache-Control': 'no-store',
        'Retry-After': '2',
        'X-Bloud-Loading': 'hermes',
      });
      res.end(page);
      return;
    }

    // The app is back, and every path below is the app answering for itself.
    // Each branch has to terminate: a redirect that redirects to itself is the
    // browser's "too many redirects" error, not a login page.
    if (url.pathname === '/auth/login') {
      res.writeHead(302, { Location: `${issuerOrigin}/application/o/authorize/?client_id=hermes` });
      res.end();
      return;
    }
    if (url.pathname === '/login') {
      res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      res.end('<!doctype html><html><body><p id="local">Hermes sign-in</p></body></html>');
      return;
    }

    switch (answer) {
      case 'cross-origin-redirect':
        // Hermes once it is serving again: it sends you to the identity
        // provider, and the second hop leaves the origin.
        res.writeHead(302, { Location: '/auth/login?provider=self-hosted' });
        break;
      case 'same-origin-redirect':
        res.writeHead(302, { Location: '/login' });
        break;
      case 'serving':
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
        res.end('<!doctype html><html><body><p id="app">Hermes</p></body></html>');
        return;
    }
    res.end();
  });

  const appPort = await listen(app);
  const appOrigin = `http://127.0.0.1:${appPort}`;
  return {
    appOrigin,
    issuerOrigin,
    waitedURL: `${appOrigin}${DEEP_LINK}`,
    async close() {
      await Promise.all([close(app), close(issuer)]);
    },
  };
}

/** Everything one page load produced, counted from the browser side. */
function watch(page: Page, deployment: Deployment) {
  const consoleErrors: string[] = [];
  /** Document requests for the URL the page is waiting on. */
  const waitedNavigations: string[] = [];
  /** Requests the page's own poll made against the URL it waits for. */
  const polls: string[] = [];
  /** Polls that reached the identity provider, which is the bug. */
  const providerPolls: string[] = [];

  page.on('console', (message) => {
    const text = message.text();
    // The two wordings of a refused cross-origin response: plain CORS, and the
    // Private Network Access refusal a local page hits when it dials out.
    if (/access to fetch|failed to fetch|cors|cross-origin/i.test(text)) {
      consoleErrors.push(`${message.type()}: ${text}`);
    }
  });
  page.on('pageerror', (error) => consoleErrors.push(`pageerror: ${error.message}`));
  page.on('request', (request) => {
    const url = request.url();
    const type = request.resourceType();
    if (type === 'document' && url.startsWith(deployment.waitedURL)) {
      waitedNavigations.push(url);
    }
    if (type === 'fetch' || type === 'xhr') {
      if (url.startsWith(deployment.waitedURL)) polls.push(url);
      if (url.startsWith(deployment.issuerOrigin)) providerPolls.push(url);
    }
  });

  return { consoleErrors, waitedNavigations, polls, providerPolls };
}

test.describe('app waiting page', () => {
  test('a redirect to the identity provider ends the wait', async ({ page }) => {
    const deployment = await deploy(await waitingPage(), 'cross-origin-redirect');
    const seen = watch(page, deployment);
    try {
      await page.goto(deployment.waitedURL);
      await expect(page.locator('#wait-message')).toContainText('Waiting for Hermes');

      // The visitor reaches the sign-in page they were always headed to, rather
      // than a spinner that promises a reload which never comes.
      await expect(page).toHaveURL(/\/application\/o\/authorize/, { timeout: 20_000 });

      // One poll saw the app answering, and that was enough. In cors mode this
      // is where the count runs on: every redirect rejects, so the wait never
      // ends and the page sits on "re-loading" over a healthy app.
      expect(seen.polls).toHaveLength(POLLS_BEFORE_APP_ANSWERS + 1);
      // The poll never dialed the identity provider: asking for the redirect
      // stops at the hop instead of following it into a cross-origin request.
      expect(seen.providerPolls).toEqual([]);
      expect(seen.consoleErrors).toEqual([]);
    } finally {
      await deployment.close();
    }
  });

  test('the reload stays on the deep link the visitor asked for', async ({ page }) => {
    const deployment = await deploy(await waitingPage(), 'cross-origin-redirect');
    const seen = watch(page, deployment);
    try {
      await page.goto(deployment.waitedURL);
      await expect(page).toHaveURL(/\/application\/o\/authorize/, { timeout: 20_000 });

      // Reload, not navigate: the page asked for the URL it is waiting on twice,
      // the navigation that got it and the reload that ended the wait, and both
      // carried the query string the visitor arrived with.
      expect(seen.waitedNavigations).toEqual([deployment.waitedURL, deployment.waitedURL]);
    } finally {
      await deployment.close();
    }
  });

  test('an app that answers without a redirect still ends the wait', async ({ page }) => {
    const deployment = await deploy(await waitingPage(), 'serving');
    const seen = watch(page, deployment);
    try {
      await page.goto(deployment.waitedURL);
      await expect(page.locator('#app')).toBeVisible({ timeout: 20_000 });

      expect(seen.polls).toHaveLength(POLLS_BEFORE_APP_ANSWERS + 1);
      expect(seen.consoleErrors).toEqual([]);
    } finally {
      await deployment.close();
    }
  });

  test('a redirect that stays on the app ends the wait too', async ({ page }) => {
    const deployment = await deploy(await waitingPage(), 'same-origin-redirect');
    try {
      await page.goto(deployment.waitedURL);
      await expect(page.locator('#local')).toBeVisible({ timeout: 20_000 });
    } finally {
      await deployment.close();
    }
  });

  test('the page keeps waiting while the app is down', async ({ page }) => {
    const deployment = await deploy(await waitingPage(), 'never');
    const seen = watch(page, deployment);
    try {
      await page.goto(deployment.waitedURL);
      await expect(page.locator('#wait-message')).toContainText('Waiting for Hermes');

      // Long enough for three polls at the page's own interval.
      await page.waitForTimeout(5_000);

      expect(seen.polls.length).toBeGreaterThanOrEqual(2);
      // Still the waiting page, still on the deep link, and no second
      // navigation: an app that has not come back must not move the tab.
      expect(page.url()).toBe(deployment.waitedURL);
      expect(seen.waitedNavigations).toEqual([deployment.waitedURL]);
      expect(seen.consoleErrors).toEqual([]);
    } finally {
      await deployment.close();
    }
  });
});
