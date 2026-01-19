// Service Worker Types and Constants
// Extracted to enable type safety across modules

// =============================================================================
// Constants (as const for type inference)
// =============================================================================

/** Request actions returned by getRequestAction */
export const RequestAction = {
  PASSTHROUGH: 'passthrough',
  FETCH: 'fetch',
} as const;

/** Request types for fetch actions */
export const RequestType = {
  EMBED: 'embed',
  ROOT: 'root',
} as const;

/** Reasons for passthrough (not intercepting) */
export const PassthroughReason = {
  CROSS_ORIGIN: 'cross-origin',
  BLOUD_ROUTE: 'bloud-route',
  EMBED_NO_REWRITE: 'embed-no-rewrite',
  NO_APP_CONTEXT: 'no-app-context',
} as const;

/** Request modes (from Fetch API) */
export const RequestMode = {
  NAVIGATE: 'navigate',
  CORS: 'cors',
  SAME_ORIGIN: 'same-origin',
} as const;

/** HTTP methods */
export const HttpMethod = {
  GET: 'GET',
  HEAD: 'HEAD',
} as const;

/** Path constants */
export const EMBED_PATH_PREFIX = '/embed/';
export const OAUTH_CALLBACK_PATH = 'openid/callback';

/** Service worker script paths that embedded apps might try to register */
export const SW_SCRIPT_PATHS = new Set(['/sw.js', '/service-worker.js']);

/** Content types */
export const CONTENT_TYPE_JS = 'application/javascript';

/** First path segments reserved for Bloud routes (O(1) lookup) */
export const RESERVED_SEGMENTS = new Set([
  'api',
  'apps',
  'catalog',
  'versions',
  'icons',
  'images',
  '_app',
  'node_modules',
  'src',
  '.svelte-kit',
  // Authentik SSO routes - must not be rewritten
  'outpost.goauthentik.io', // Embedded outpost OAuth start/callback
  'application', // OAuth2/OIDC endpoints
  'flows', // Authentik authentication flows (/flows/-/default/...)
  'if', // Authentik Identity Frontend UI
  '-', // Authentik internal API
  'static', // Authentik static assets (embedded apps use /embed/{app}/static/)
]);

// =============================================================================
// Type Definitions
// =============================================================================

/** Values from RequestAction constant */
export type RequestActionValue = (typeof RequestAction)[keyof typeof RequestAction];

/** Values from RequestType constant */
export type RequestTypeValue = (typeof RequestType)[keyof typeof RequestType];

/** Values from PassthroughReason constant */
export type PassthroughReasonValue = (typeof PassthroughReason)[keyof typeof PassthroughReason];

/** Values from RequestMode constant */
export type RequestModeValue = (typeof RequestMode)[keyof typeof RequestMode];

/** Result from shouldHandleRequest */
export interface HandleRequestDecision {
  handle: boolean;
  reason?: PassthroughReasonValue;
  type?: RequestTypeValue;
  appName?: string;
}

/** Result from getRequestAction */
export interface RequestActionResult {
  action: RequestActionValue;
  reason?: PassthroughReasonValue;
  type?: RequestTypeValue;
  fetchUrl?: string;
  appName?: string;
}

/** Minimal response interface for testing (duck typing) */
export interface ResponseLike {
  type?: string;
  status: number;
  headers: {
    get(name: string): string | null;
  };
}

/** Message types for postMessage communication */
export const MessageType = {
  SET_ACTIVE_APP: 'SET_ACTIVE_APP',
  SET_INTERCEPTS: 'SET_INTERCEPTS',
} as const;
