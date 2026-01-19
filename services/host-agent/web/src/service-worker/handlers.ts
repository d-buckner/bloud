// Service Worker Request Handlers
// Functions that use Service Worker APIs (FetchEvent, Response, etc.)

/// <reference lib="webworker" />

import {
  RequestAction,
  RequestType,
  RequestMode,
  HttpMethod,
  CONTENT_TYPE_JS,
} from './types';

import {
  getRequestAction,
  isServiceWorkerScript,
  shouldRedirectOAuthCallback,
  processRedirectResponse,
  getActiveApp,
  getInterceptConfig,
  registerClient,
  getAppForClient,
  getAppFromReferer,
  appNeedsRewrite,
  rewriteRootUrl,
  isAuthRedirect,
  PassthroughReason,
} from './core';

import { EMBED_PATH_PREFIX } from './types';

import { injectIntoHtml } from './inject';

// Re-export state setters for index.ts message handler
export { setActiveApp, setInterceptConfig } from './core';

declare const self: ServiceWorkerGlobalScope;

// =============================================================================
// Fetch Options Builders
// =============================================================================

/**
 * Build fetch options for a root-level request
 */
async function buildFetchOptions(request: Request): Promise<RequestInit> {
  const options: RequestInit = {
    method: request.method,
    headers: request.headers,
    credentials: request.credentials,
    cache: request.cache,
  };

  // 'navigate' mode can't be used with fetch() - use 'same-origin' instead
  if (request.mode === RequestMode.NAVIGATE) {
    options.mode = RequestMode.SAME_ORIGIN;
  } else if (request.mode) {
    options.mode = request.mode as RequestMode;
  }

  // For requests with body, read it into ArrayBuffer to avoid stream issues
  const hasBody =
    request.method !== HttpMethod.GET &&
    request.method !== HttpMethod.HEAD &&
    request.body;

  if (!hasBody) {
    return options;
  }

  try {
    const clonedRequest = request.clone();
    options.body = await clonedRequest.arrayBuffer();
  } catch (e) {
    console.error('[embed-sw] Failed to read request body:', (e as Error).message);
  }

  return options;
}

// =============================================================================
// Request Handlers
// =============================================================================

/**
 * Handle a root-level request by fetching from embed path and returning
 */
function handleRootRequest(
  event: FetchEvent,
  request: Request,
  fetchUrl: string
): void {
  const doFetch = async (): Promise<Response> => {
    const fetchOptions = await buildFetchOptions(request);
    const response = await fetch(fetchUrl, fetchOptions);

    // CRITICAL: Create a new Response to strip the .url property from the fetched response.
    // Otherwise, the browser uses response.url for import.meta.url resolution,
    // which would be the embed path instead of the original request path.
    const blob = await response.blob();
    return new Response(blob, {
      status: response.status,
      statusText: response.statusText,
      headers: response.headers,
    });
  };

  event.respondWith(
    doFetch().catch((error) => {
      console.error(
        '[embed-sw] Fetch error for',
        fetchUrl,
        ':',
        error.message,
        error.stack
      );
      throw error;
    })
  );
}

/**
 * Check if a response is HTML based on Content-Type header
 */
function isHtmlResponse(response: Response): boolean {
  const contentType = response.headers.get('content-type') || '';
  return contentType.includes('text/html');
}

/**
 * Inject storage intercept script into HTML response if configured
 */
async function maybeInjectIntercepts(response: Response): Promise<Response> {
  const config = getInterceptConfig();

  // No injection needed if no config or not HTML
  if (!config || !isHtmlResponse(response)) {
    return response;
  }

  const html = await response.text();
  const injectedHtml = injectIntoHtml(html, config);

  console.log('[embed-sw] Injected storage intercepts into HTML response');

  return new Response(injectedHtml, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
  });
}

/**
 * Create an HTML page that redirects the top-level window to Authentik's OAuth start endpoint.
 * Uses Authentik's `rd` parameter to specify where to redirect after successful auth.
 *
 * This is needed because:
 * 1. Cross-origin OAuth flows don't work well in iframes
 * 2. We need to redirect the TOP-LEVEL window, not the iframe
 */
function createTopLevelRedirectPage(returnPath: string): Response {
  // Use Authentik's /start endpoint with rd parameter - this is the standard way
  // to start an OAuth flow with a specific redirect destination
  const startUrl = `/outpost.goauthentik.io/start?rd=${encodeURIComponent(returnPath)}`;

  const html = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Redirecting to login...</title>
  <style>
    body {
      font-family: system-ui, -apple-system, sans-serif;
      display: flex;
      align-items: center;
      justify-content: center;
      height: 100vh;
      margin: 0;
      background: #1a1a1a;
      color: #e0e0e0;
    }
    .container { text-align: center; }
    .spinner {
      width: 32px;
      height: 32px;
      border: 2px solid #333;
      border-top-color: #3b82f6;
      border-radius: 50%;
      animation: spin 0.8s linear infinite;
      margin: 0 auto 16px;
    }
    @keyframes spin { to { transform: rotate(360deg); } }
  </style>
</head>
<body>
  <div class="container">
    <div class="spinner"></div>
    <p>Redirecting to login...</p>
  </div>
  <script>
    (function() {
      const startUrl = ${JSON.stringify(startUrl)};
      console.log('[embed-sw-redirect] Redirecting to Authentik start:', startUrl);

      // Redirect the top-level window to start the OAuth flow
      if (window.top !== window.self) {
        window.top.location.href = startUrl;
      } else {
        window.location.href = startUrl;
      }
    })();
  </script>
</body>
</html>`;

  return new Response(html, {
    status: 200,
    headers: {
      'Content-Type': 'text/html; charset=utf-8',
      'X-Frame-Options': 'SAMEORIGIN',
      'Content-Security-Policy': "frame-ancestors 'self'",
      'Cross-Origin-Resource-Policy': 'same-origin',
      'Cross-Origin-Embedder-Policy': 'credentialless',
    },
  });
}

/**
 * Handle an embed navigation request with redirect rewriting
 */
function handleEmbedNavigationRequest(
  event: FetchEvent,
  request: Request,
  fetchUrl: string,
  appName: string,
  origin: string
): void {
  console.log('[embed-sw] handleEmbedNavigationRequest:', { fetchUrl, appName });

  const doFetch = async (): Promise<Response> => {
    console.log('[embed-sw] doFetch starting for:', fetchUrl);

    // Use redirect: 'manual' to intercept redirects before they happen
    // This allows us to detect auth redirects and handle them specially
    const response = await fetch(fetchUrl, {
      method: request.method,
      headers: request.headers,
      credentials: request.credentials,
      redirect: 'manual',
    });

    console.log('[embed-sw] Embed response:', {
      status: response.status,
      type: response.type,
      ok: response.ok,
    });

    // Check for opaqueredirect - this means a cross-origin redirect happened
    // (rare case, kept for edge cases where apps redirect to external URLs)
    if (response.type === 'opaqueredirect') {
      const returnPath = `/apps/${appName}`;
      console.log('[embed-sw] Opaqueredirect detected, return path:', returnPath);
      return createTopLevelRedirectPage(returnPath);
    }

    // Check for redirects (status 3xx with Location header)
    // Auth redirects go to /auth/* (same-origin) and require top-level window redirect
    if (response.status >= 300 && response.status < 400) {
      const location = response.headers.get('Location');
      console.log('[embed-sw] Redirect response:', { status: response.status, location });

      if (location) {
        const isAuth = isAuthRedirect(location, origin);
        if (isAuth) {
          // Auth redirect to /auth/* - redirect top-level window for login flow
          const returnPath = `/apps/${appName}`;
          console.log('[embed-sw] Auth redirect detected, return path:', returnPath);
          return createTopLevelRedirectPage(returnPath);
        }

        // For same-origin redirects, rewrite to stay within /embed/{appName}/ namespace
        const newLocation = processRedirectResponse(response, appName, origin);
        if (newLocation) {
          return Response.redirect(newLocation, response.status);
        }
      }
    }

    // Inject IndexedDB intercepts into HTML responses
    return maybeInjectIntercepts(response);
  };

  event.respondWith(doFetch());
}

// =============================================================================
// Main Request Handler
// =============================================================================

/**
 * Service Worker fetch event handler.
 * Uses SW-specific APIs (self, clients) - not directly testable,
 * but all logic delegates to testable pure functions.
 */
export function handleRequest(event: FetchEvent): void {
  const request = event.request;
  const url = new URL(request.url);
  const origin = self.location.origin;
  const activeApp = getActiveApp();

  // Block embedded apps from registering their own service workers
  if (isServiceWorkerScript(url.pathname) && activeApp) {
    event.respondWith(
      new Response('// SW disabled for embedded apps', {
        status: 200,
        headers: { 'Content-Type': CONTENT_TYPE_JS },
      })
    );
    return;
  }

  // ClientId tracking: if this request comes from a registered app iframe, rewrite it.
  // This is authoritative - ignores RESERVED_SEGMENTS since apps like Radarr use /api/v3/.
  const clientApp = getAppForClient(event.clientId);

  if (clientApp && appNeedsRewrite(clientApp) && url.origin === origin) {
    const isAuthentikRoute = url.pathname.startsWith('/outpost.goauthentik.io');
    if (!url.pathname.startsWith(EMBED_PATH_PREFIX) && !isAuthentikRoute) {
      const fetchUrl = rewriteRootUrl(url, clientApp);
      console.log('[embed-sw] Rewriting via clientId:', { clientApp, from: url.pathname });
      handleRootRequest(event, request, fetchUrl);
      return;
    }
  }

  const result = getRequestAction(url, origin);

  if (result.action === RequestAction.PASSTHROUGH) {
    // Fallback: check Referer header for web workers where clientId isn't registered
    if (result.reason === PassthroughReason.NO_APP_CONTEXT) {
      const referer = request.headers.get('Referer');
      const refererApp = getAppFromReferer(referer, origin);

      if (refererApp && appNeedsRewrite(refererApp)) {
        const fetchUrl = rewriteRootUrl(url, refererApp);
        handleRootRequest(event, request, fetchUrl);
        return;
      }
    }
    return;
  }

  // Root-level requests (not under /embed/)
  if (result.type === RequestType.ROOT) {
    if (shouldRedirectOAuthCallback(request.mode, url.pathname)) {
      event.respondWith(Response.redirect(result.fetchUrl!, 302));
      return;
    }
    handleRootRequest(event, request, result.fetchUrl!);
    return;
  }

  // Embed navigation requests only - static assets pass through
  if (request.mode !== RequestMode.NAVIGATE) {
    return;
  }

  // Register clientId for future request tracking
  if (event.resultingClientId && result.appName) {
    registerClient(event.resultingClientId, result.appName);
  }

  handleEmbedNavigationRequest(
    event,
    request,
    result.fetchUrl!,
    result.appName!,
    origin
  );
}
