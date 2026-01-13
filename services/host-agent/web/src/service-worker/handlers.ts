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
  getProtectedEntries,
} from './core';

import { injectIntoHtml } from './inject';

// Re-export state setters for index.ts message handler
export { setActiveApp, setProtectedEntries } from './core';

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

/**
 * Build fetch options for an embed navigation request
 */
function buildEmbedFetchOptions(request: Request): RequestInit {
  const options: RequestInit = {
    method: request.method,
    headers: request.headers,
    body: request.body,
    credentials: request.credentials,
    cache: request.cache,
    redirect: 'manual',
  };

  if (request.body) {
    (options as RequestInit & { duplex: string }).duplex = 'half';
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
 * Check if a response is HTML based on content-type header
 */
function isHtmlResponse(response: Response): boolean {
  const contentType = response.headers.get('content-type') ?? '';
  return contentType.includes('text/html');
}

/**
 * Handle an embed navigation request with redirect rewriting and script injection
 */
function handleEmbedNavigationRequest(
  event: FetchEvent,
  request: Request,
  fetchUrl: string,
  appName: string,
  origin: string
): void {
  const doFetch = async (): Promise<Response> => {
    const fetchOptions = buildEmbedFetchOptions(request);
    const response = await fetch(fetchUrl, fetchOptions);
    const newLocation = processRedirectResponse(response, appName, origin);

    if (newLocation) {
      return Response.redirect(newLocation, response.status || 302);
    }

    // Inject IndexedDB protection script into HTML responses
    const protectedEntries = getProtectedEntries(appName);
    if (protectedEntries.length > 0 && isHtmlResponse(response)) {
      const html = await response.text();
      const injectedHtml = injectIntoHtml(html, protectedEntries);
      console.log('[embed-sw] Injected IndexedDB protection for:', appName);
      return new Response(injectedHtml, {
        status: response.status,
        statusText: response.statusText,
        headers: response.headers,
      });
    }

    return response;
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

  console.log('[embed-sw] handleRequest called:', {
    url: url.pathname,
    mode: request.mode,
    destination: request.destination,
    origin: origin,
    activeApp: activeApp,
  });

  // Block embedded apps from registering their own service workers
  if (isServiceWorkerScript(url.pathname) && activeApp) {
    console.log(
      '[embed-sw] Blocking SW registration for embedded app:',
      activeApp
    );
    event.respondWith(
      new Response('// SW disabled for embedded apps', {
        status: 200,
        headers: { 'Content-Type': CONTENT_TYPE_JS },
      })
    );
    return;
  }

  const result = getRequestAction(url, origin);

  if (result.action === RequestAction.PASSTHROUGH) {
    console.log(
      '[embed-sw] PASSTHROUGH - not intercepting:',
      url.pathname,
      'reason:',
      result.reason
    );
    return;
  }

  console.log(
    '[embed-sw] INTERCEPTING:',
    url.pathname,
    'action:',
    result.action,
    'type:',
    result.type,
    'fetchUrl:',
    result.fetchUrl
  );

  // Handle root-level requests (not under /embed/)
  if (result.type === RequestType.ROOT) {
    console.log(
      '[embed-sw] ROOT type - calling respondWith for:',
      url.pathname,
      '-> fetching from:',
      result.fetchUrl
    );

    // For OAuth callback navigation requests, use redirect instead of fetch-and-return
    if (shouldRedirectOAuthCallback(request.mode, url.pathname)) {
      console.log(
        '[embed-sw] OAuth callback navigation - using redirect instead of fetch'
      );
      event.respondWith(Response.redirect(result.fetchUrl!, 302));
      return;
    }

    handleRootRequest(event, request, result.fetchUrl!);
    return;
  }

  // Embed request (URL already under /embed/appName/)
  // Only handle navigation requests - static assets pass through directly
  if (request.mode !== RequestMode.NAVIGATE) {
    return;
  }

  handleEmbedNavigationRequest(
    event,
    request,
    result.fetchUrl!,
    result.appName!,
    origin
  );
}
