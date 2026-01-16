// Core logic for embedded app URL rewriting
// Pure functions with no Service Worker API dependencies - fully testable
//
// The key insight: we intercept /embed/{appName}/ requests and handle redirects
// manually, rewriting redirect URLs to stay within /embed/{appName}/ namespace.
// This preserves the app context even when apps redirect to absolute paths.

import {
  RequestAction,
  RequestType,
  PassthroughReason,
  RequestMode,
  EMBED_PATH_PREFIX,
  OAUTH_CALLBACK_PATH,
  AUTHENTIK_STATIC_PREFIX,
  SW_SCRIPT_PATHS,
  REWRITE_APPS,
  RESERVED_SEGMENTS,
  type HandleRequestDecision,
  type RequestActionResult,
  type ResponseLike,
} from './types';

// Re-export constants and types for consumers
export {
  RequestAction,
  RequestType,
  PassthroughReason,
  RequestMode,
  REWRITE_APPS,
  RESERVED_SEGMENTS,
} from './types';

export type {
  HandleRequestDecision,
  RequestActionResult,
  ResponseLike,
} from './types';

// =============================================================================
// Active App Context (module state)
// =============================================================================

import type { InterceptConfig } from './inject';

let activeAppContext: string | null = null;
let interceptConfig: InterceptConfig | null = null;

// Map clientId -> appName for tracking which iframe/worker belongs to which app
const clientAppMap = new Map<string, string>();

/** Reset function for testing - clears module state */
export function resetTestState(): void {
  activeAppContext = null;
  interceptConfig = null;
  clientAppMap.clear();
}

/**
 * Set the active app context (called from message handler).
 * This is the single source of truth for which app is currently open.
 */
export function setActiveApp(appName: string | null): void {
  activeAppContext = appName;
}

/** Get the active app context (for testing) */
export function getActiveApp(): string | null {
  return activeAppContext;
}

/**
 * Set the storage intercept configuration (called from message handler).
 * This config is used to inject intercept scripts into iframe HTML responses.
 */
export function setInterceptConfig(config: InterceptConfig | null): void {
  interceptConfig = config;
}

/** Get the current intercept configuration */
export function getInterceptConfig(): InterceptConfig | null {
  return interceptConfig;
}

// =============================================================================
// Client ID Tracking (for iframes and web workers)
// =============================================================================

/**
 * Register a clientId as belonging to an app.
 * Called when we handle embed navigation requests.
 */
export function registerClient(clientId: string, appName: string): void {
  clientAppMap.set(clientId, appName);
}

/**
 * Get the app name for a clientId.
 */
export function getAppForClient(clientId: string | undefined): string | null {
  if (!clientId) return null;
  return clientAppMap.get(clientId) ?? null;
}

/**
 * Extract app name from Referer header (fallback for web workers).
 * Web workers have their own clientId that isn't registered,
 * but their Referer points back to the parent page.
 */
export function getAppFromReferer(referer: string | null, origin: string): string | null {
  if (!referer) return null;

  try {
    const refererUrl = new URL(referer);
    // Only trust same-origin referers
    if (refererUrl.origin !== origin) return null;

    return getAppFromPath(refererUrl.pathname);
  } catch {
    return null;
  }
}

/** Get client map size (for testing) */
export function getClientMapSize(): number {
  return clientAppMap.size;
}

// =============================================================================
// Path Analysis Functions
// =============================================================================

/**
 * Extract app name from a URL path like /embed/actual-budget/... or /apps/actual-budget/...
 */
export function getAppFromPath(pathname: string): string | null {
  const match = pathname.match(/^\/(embed|apps)\/([^/]+)/);
  return match ? match[2] : null;
}

/**
 * Check if a path is a Bloud route (not an embedded app) - O(1)
 */
export function isBloudRoute(pathname: string): boolean {
  // Root path is always a Bloud route (home page)
  if (pathname === '/') {
    return true;
  }

  // Special case: /@ prefix (Vite dev server)
  if (pathname.length > 1 && pathname[1] === '@') {
    return true;
  }

  // Authentik static assets at /static/dist/ - NOT embedded app assets
  if (pathname.startsWith(AUTHENTIK_STATIC_PREFIX)) {
    return true;
  }

  // Extract first segment: /api/foo -> api, /catalog -> catalog
  const secondSlash = pathname.indexOf('/', 1);
  const firstSegment =
    secondSlash === -1 ? pathname.slice(1) : pathname.slice(1, secondSlash);

  return RESERVED_SEGMENTS.has(firstSegment);
}

/**
 * Check if request is for a service worker script (embedded apps shouldn't register their own SW)
 */
export function isServiceWorkerScript(pathname: string): boolean {
  return SW_SCRIPT_PATHS.has(pathname);
}

// =============================================================================
// Request Handling Decision Functions
// =============================================================================

/**
 * Check if a request should be handled by the service worker
 */
export function shouldHandleRequest(
  url: URL,
  origin: string
): HandleRequestDecision {
  // Only handle same-origin requests
  if (url.origin !== origin) {
    return { handle: false, reason: PassthroughReason.CROSS_ORIGIN };
  }

  // Never touch Bloud's own routes
  if (isBloudRoute(url.pathname)) {
    return { handle: false, reason: PassthroughReason.BLOUD_ROUTE };
  }

  // Handle /embed/{appName}/... requests for rewrite apps
  if (!url.pathname.startsWith(EMBED_PATH_PREFIX)) {
    // Root-level requests (need to determine app from context)
    return { handle: true, type: RequestType.ROOT };
  }

  // URL is under /embed/
  const appName = getAppFromPath(url.pathname);
  if (!appName || !REWRITE_APPS.has(appName)) {
    return { handle: false, reason: PassthroughReason.EMBED_NO_REWRITE };
  }

  // Only handle navigation requests (HTML pages that might redirect).
  // Static assets at /embed/ don't need interception.
  return { handle: true, type: RequestType.EMBED, appName };
}

/**
 * Check if an OAuth callback navigation request should use redirect instead of fetch-and-return.
 *
 * This works around a browser/SW context issue where internal fetch() from the SW
 * fails with 404 for OAuth callbacks, but browser direct navigation works correctly.
 * Using redirect causes the browser to make the request directly.
 */
export function shouldRedirectOAuthCallback(
  requestMode: string | undefined | null,
  pathname: string
): boolean {
  return (
    requestMode === RequestMode.NAVIGATE && pathname.includes(OAUTH_CALLBACK_PATH)
  );
}

/**
 * Compute what action to take for a request. (Testable, pure function)
 */
export function getRequestAction(url: URL, origin: string): RequestActionResult {
  const decision = shouldHandleRequest(url, origin);

  if (!decision.handle) {
    return { action: RequestAction.PASSTHROUGH, reason: decision.reason };
  }

  if (decision.type === RequestType.EMBED) {
    return {
      action: RequestAction.FETCH,
      type: RequestType.EMBED,
      fetchUrl: url.href,
      appName: decision.appName,
    };
  }

  // Root request - determine app from active context (set by main frame via postMessage)
  const appName = activeAppContext;

  // Only rewrite for apps that need it
  if (!appName || !REWRITE_APPS.has(appName)) {
    return { action: RequestAction.PASSTHROUGH, reason: PassthroughReason.NO_APP_CONTEXT };
  }

  return {
    action: RequestAction.FETCH,
    type: RequestType.ROOT,
    fetchUrl: rewriteRootUrl(url, appName),
    appName,
  };
}

// =============================================================================
// URL Rewriting Functions
// =============================================================================

/**
 * Rewrite a redirect location to stay within /embed/{appName}/ namespace
 */
export function rewriteRedirectLocation(
  location: string,
  appName: string,
  origin: string
): string | null {
  const locationUrl = new URL(location, origin);

  // If already under /embed/, don't rewrite
  if (locationUrl.pathname.startsWith(EMBED_PATH_PREFIX)) {
    return null;
  }

  const newPath = `${EMBED_PATH_PREFIX}${appName}${locationUrl.pathname}${locationUrl.search}`;
  return new URL(newPath, origin).href;
}

/**
 * Rewrite a root-level request URL to include app prefix
 */
export function rewriteRootUrl(url: URL, appName: string): string {
  const newPath = `${EMBED_PATH_PREFIX}${appName}${url.pathname}`;
  return new URL(newPath + url.search, url.origin).href;
}

// =============================================================================
// Response Processing Functions
// =============================================================================

/**
 * Check if response is a redirect
 */
export function isRedirectResponse(response: ResponseLike): boolean {
  if (response.type === 'opaqueredirect') {
    return true;
  }
  return response.status >= 300 && response.status < 400;
}

/**
 * Process a response and rewrite redirect if needed. (Testable, pure function)
 */
export function processRedirectResponse(
  response: ResponseLike,
  appName: string,
  origin: string
): string | null {
  if (!isRedirectResponse(response)) {
    return null;
  }

  const location = response.headers.get('Location');
  if (!location) {
    return null;
  }

  return rewriteRedirectLocation(location, appName, origin);
}
