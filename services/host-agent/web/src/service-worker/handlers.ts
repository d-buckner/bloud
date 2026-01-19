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
  unregisterClient,
  getAppForClient,
  getAppFromReferer,
  appNeedsRewrite,
  rewriteRootUrl,
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

    // Opaque redirect means the browser is navigating away from the app
    // (likely to Authentik login). Unregister the client so subsequent
    // requests from that context (e.g., Authentik's static assets) aren't rewritten.
    if (response.type === 'opaqueredirect') {
      unregisterClient(event.clientId);
      unregisterClient(event.resultingClientId);
    }

    // Check for redirects (status 3xx with Location header)
    // Rewrite redirect URLs to stay within /embed/{appName}/ namespace
    // Auth is handled by the app/Traefik, not the service worker
    if (response.status >= 300 && response.status < 400) {
      const location = response.headers.get('Location');
      console.log('[embed-sw] Redirect response:', { status: response.status, location });

      if (location) {
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
