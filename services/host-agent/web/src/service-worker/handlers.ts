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
} from './core';

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
 * Check if a response is HTML based on Content-Type header
 */
function isHtmlResponse(response: Response): boolean {
  const contentType = response.headers.get('content-type') || '';
  return contentType.includes('text/html');
}

/**
 * Inject IndexedDB intercept script into HTML response if configured
 */
async function maybeInjectIntercepts(response: Response): Promise<Response> {
  const config = getInterceptConfig();

  // No injection needed if no config or not HTML
  if (!config || !isHtmlResponse(response)) {
    return response;
  }

  const html = await response.text();
  const injectedHtml = injectIntoHtml(html, config);

  console.log('[embed-sw] Injected IndexedDB intercepts into HTML response');

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
  const doFetch = async (): Promise<Response> => {
    const fetchOptions = buildEmbedFetchOptions(request);
    const response = await fetch(fetchUrl, fetchOptions);
    const newLocation = processRedirectResponse(response, appName, origin);

    if (newLocation) {
      return Response.redirect(newLocation, response.status || 302);
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

  const result = getRequestAction(url, origin);

  if (result.action === RequestAction.PASSTHROUGH) {
    return;
  }

  // Handle root-level requests (not under /embed/)
  if (result.type === RequestType.ROOT) {
    // For OAuth callback navigation requests, use redirect instead of fetch-and-return
    if (shouldRedirectOAuthCallback(request.mode, url.pathname)) {
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
