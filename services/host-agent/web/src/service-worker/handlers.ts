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
  isBloudRoute,
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

    // DEBUG: Log response headers for auth debugging
    const headerObj: Record<string, string> = {};
    response.headers.forEach((value, key) => {
      headerObj[key] = value;
    });
    console.log('[embed-sw] Root request response:', {
      originalUrl: request.url,
      fetchUrl,
      status: response.status,
      redirected: response.redirected,
      finalUrl: response.url,
      headers: headerObj,
    });

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

    // Use redirect: 'follow' (default) so we can see the final URL after redirects.
    // We can't use 'manual' because it returns opaqueredirect with no Location header.
    const response = await fetch(fetchUrl, {
      method: request.method,
      headers: request.headers,
      credentials: request.credentials,
      redirect: 'follow',
    });

    // Log response details including headers for debugging auth
    const headerObj: Record<string, string> = {};
    response.headers.forEach((value, key) => {
      headerObj[key] = value;
    });
    console.log('[embed-sw] Embed response:', {
      status: response.status,
      type: response.type,
      ok: response.ok,
      url: response.url,
      redirected: response.redirected,
      headers: headerObj,
    });

    // If the response was redirected, check if it went outside the embed namespace.
    // If so, we need to rewrite the URL and redirect the browser back into /embed/{app}/.
    if (response.redirected && response.url) {
      const finalUrl = new URL(response.url);

      // Check if the redirect went to a root-level path (outside /embed/)
      if (!finalUrl.pathname.startsWith(EMBED_PATH_PREFIX)) {
        // Check if it's an auth route (Authentik) - redirect browser there
        if (isBloudRoute(finalUrl.pathname)) {
          console.log('[embed-sw] Redirect to auth route, unregistering:', finalUrl.pathname);
          unregisterClient(event.clientId);
          unregisterClient(event.resultingClientId);
          // CRITICAL: We can't return the followed response directly for navigation requests.
          // The browser rejects "a redirected response was used for a request whose redirect mode is not follow".
          // Instead, redirect the browser to the auth URL so it handles authentication natively.
          console.log('[embed-sw] Redirecting browser to auth:', response.url);
          return Response.redirect(response.url, 302);
        }

        // Rewrite the redirect to stay in embed namespace
        const rewrittenPath = `${EMBED_PATH_PREFIX}${appName}${finalUrl.pathname}${finalUrl.search}`;
        const rewrittenUrl = new URL(rewrittenPath, origin).href;
        console.log('[embed-sw] Rewriting redirect:', { from: response.url, to: rewrittenUrl });
        return Response.redirect(rewrittenUrl, 302);
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

  // DEBUG: Log ALL requests to trace what SW sees
  console.log('[embed-sw] FETCH:', url.pathname, 'mode=' + request.mode, 'clientId=' + event.clientId);

  // DEBUG: Log navigation requests with full context
  if (request.mode === 'navigate') {
    console.log('[embed-sw] NAVIGATE request:', {
      url: url.pathname + url.search,
      activeApp,
      clientId: event.clientId,
      resultingClientId: event.resultingClientId,
    });
  }

  // DEBUG: Log API requests (sync, account, subscribe-set-token, etc.)
  if (url.pathname.includes('/sync') || url.pathname.includes('/account') || url.pathname.includes('subscribe')) {
    console.log('[embed-sw] API request:', {
      url: url.pathname + url.search,
      method: request.method,
      mode: request.mode,
      clientId: event.clientId,
      activeApp,
    });
  }

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

  // CRITICAL: Let OAuth callbacks bypass the service worker entirely.
  //
  // /openid/callback - Server sets session cookie, redirects to /openid-cb
  //   SW must not intercept so Set-Cookie headers are applied by the browser.
  //
  // /openid-cb - Frontend processes token from URL, stores in IndexedDB, redirects to app
  //   SW must not rewrite to /embed/{app}/ because the app router expects the root path.
  //   When redirected, the app would see /embed/{app}/openid-cb which may cause routing issues.
  //   Let the browser handle natively, then subsequent navigations will be rewritten correctly.
  if (url.pathname.includes('/openid/callback') || url.pathname === '/openid-cb') {
    console.log('[embed-sw] Letting OAuth callback bypass SW:', {
      pathname: url.pathname,
      activeApp: activeApp,
      clientId: event.clientId,
      resultingClientId: event.resultingClientId,
      mode: request.mode,
    });

    // IMPORTANT: Even though we bypass the navigation, we must register the resultingClientId
    // so that subsequent asset requests (e.g., /static/js/...) from this page get rewritten.
    // Without this, assets 404 because /static is in RESERVED_SEGMENTS.
    if (event.resultingClientId && activeApp) {
      registerClient(event.resultingClientId, activeApp);
      console.log('[embed-sw] Registered bypassed OAuth client:', {
        resultingClientId: event.resultingClientId,
        appName: activeApp,
      });
    }

    return;
  }

  // DEBUG: Log all OAuth-related requests
  if (url.pathname.includes('openid') || url.pathname.includes('oauth') || url.pathname.includes('token')) {
    console.log('[embed-sw] OAuth-related request:', {
      pathname: url.pathname,
      search: url.search,
      mode: request.mode,
      clientId: event.clientId,
      resultingClientId: event.resultingClientId,
      activeApp: activeApp,
    });
  }

  // ClientId tracking: if this request comes from a registered app iframe, rewrite it.
  // This is authoritative - ignores RESERVED_SEGMENTS since apps like Radarr use /api/v3/.
  let clientApp = getAppForClient(event.clientId);

  // For navigation requests, check if client is navigating to a reserved route
  // (e.g., Authentik SSO at /application/o/authorize/). If so, unregister the client
  // since they're leaving the app context and we shouldn't rewrite subsequent requests.
  if (request.mode === RequestMode.NAVIGATE && clientApp && isBloudRoute(url.pathname)) {
    console.log('[embed-sw] Client navigating to reserved route, unregistering:', {
      clientApp,
      destination: url.pathname,
    });
    unregisterClient(event.clientId);
    clientApp = null;
  }

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

  // Debug: log requests that might be from workers
  if (url.pathname.includes('kcab') || url.pathname.includes('worker')) {
    console.log('[embed-sw] Worker-related request:', {
      pathname: url.pathname,
      clientId: event.clientId,
      clientApp,
      result: result.action,
      type: result.type,
      fetchUrl: result.fetchUrl,
      reason: result.reason,
      referer: request.headers.get('Referer'),
    });
  }

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
    // For navigation requests, redirect to embed path instead of fetching.
    // This ensures the browser URL is at /embed/{app}/... so subsequent
    // asset requests (like /static/...) get properly rewritten.
    if (request.mode === RequestMode.NAVIGATE) {
      console.log('[embed-sw] Redirecting root navigation to embed path:', {
        from: url.pathname,
        search: url.search,
        to: result.fetchUrl,
        referer: request.headers.get('Referer'),
        cookie: request.headers.get('Cookie'),
      });
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
