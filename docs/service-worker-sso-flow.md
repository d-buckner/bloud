# Service Worker SSO Flow

This document captures the findings and implementation details for handling SSO (Single Sign-On) flows within the embedded app service worker architecture.

## Problem Statement

When an embedded app (e.g., Actual Budget) triggers an SSO login flow, the user navigates outside the `/embed/{app}/` namespace to Authentik's authorization endpoint. After authentication, the OAuth callback and subsequent redirects need to be handled correctly to keep the iframe within the embed namespace so assets load properly.

## Architecture Overview

### URL Namespaces

- `/embed/{app}/` - Embedded app routes, served by Traefik with prefix stripping
- `/application/`, `/flows/`, `/if/`, etc. - Authentik SSO routes (reserved)
- `/api/`, `/apps/`, `/catalog/`, etc. - Bloud platform routes (reserved)

### Service Worker Tracking Mechanisms

1. **`clientAppMap`** - Maps browser clientId to app name. Tracks which browser context (tab/iframe) belongs to which app.

2. **`activeAppContext`** - Global state set via postMessage from the main Bloud frame. Indicates which app is currently displayed in the UI.

### Traefik Routing

- Requests to `/embed/{app}/...` have the prefix stripped before forwarding to the app
- Apps with `absolutePaths` config get additional root-level routes (e.g., `/openid/*` for OAuth callbacks)

## The SSO Flow

### Step 1: User Initiates SSO

1. User is at `/embed/actual-budget/` in an iframe
2. clientId is registered in `clientAppMap` as `actual-budget`
3. User clicks "Login with SSO"

### Step 2: Navigation to Authentik

1. App navigates to `/application/o/authorize/?client_id=...`
2. SW intercepts navigation request
3. SW detects: `request.mode === 'navigate'` AND `clientApp` is registered AND destination is a reserved route (`/application/` is in `RESERVED_SEGMENTS`)
4. SW unregisters the clientId - this client is leaving the app context
5. Request passes through to Authentik

### Step 3: Authentik Authentication

1. User authenticates with Authentik
2. Authentik redirects to `/openid/callback?code=...` (root level)

### Step 4: OAuth Callback Handling

1. SW intercepts the callback navigation
2. `activeAppContext` is still set to `actual-budget`
3. SW rewrites `/openid/callback` to `/embed/actual-budget/openid/callback`
4. Browser navigates to the rewritten URL

### Step 5: App Processes Callback

1. SW intercepts navigation to `/embed/actual-budget/openid/callback`
2. SW fetches from Traefik (which strips prefix and forwards to app)
3. App processes the OAuth code and responds with a redirect to `/openid-cb?token=...`
4. **Critical**: The redirect is to a root-level URL, outside `/embed/`

### Step 6: Redirect Rewriting

1. SW detects the redirect went outside `/embed/` namespace
2. SW rewrites the redirect to `/embed/actual-budget/openid-cb?token=...`
3. Browser navigates to the rewritten URL
4. App loads correctly with assets served from `/embed/actual-budget/static/...`

## Technical Findings

### `redirect: 'manual'` Returns Opaque Responses

When using `fetch()` with `redirect: 'manual'`, the browser returns an `opaqueredirect` response with:
- `status: 0`
- `type: 'opaqueredirect'`
- `headers`: empty (no Location header readable)

This is by design per the [Fetch Standard](https://fetch.spec.whatwg.org/) to prevent information leakage. It happens for **both** same-origin and cross-origin redirects.

**Solution**: Use `redirect: 'follow'` (default) and check `response.url` and `response.redirected` after the fetch completes to detect redirects.

### Reserved Segments

The following path segments are reserved for Bloud/Authentik routes and should not be rewritten:

```typescript
export const RESERVED_SEGMENTS = new Set([
  'api', 'apps', 'catalog', 'versions', 'icons', 'images',
  '_app', 'node_modules', 'src', '.svelte-kit',
  // Authentik SSO routes
  'outpost.goauthentik.io', 'application', 'flows', 'if', '-', 'static',
]);
```

### Client Unregistration

When a registered client navigates to a reserved route (e.g., Authentik SSO), we unregister the clientId to prevent subsequent requests (like Authentik's static assets) from being incorrectly rewritten.

```typescript
if (request.mode === 'navigate' && clientApp && isBloudRoute(url.pathname)) {
  unregisterClient(event.clientId);
  clientApp = null;
}
```

## Implementation

### handlers.ts Changes

1. **Client unregistration on SSO navigation** (lines 243-253):
   - Detect navigation to reserved routes
   - Unregister clientId to stop rewriting

2. **Redirect following and rewriting** (lines 168-207):
   - Use `redirect: 'follow'` instead of `redirect: 'manual'`
   - Check `response.redirected` and `response.url` after fetch
   - If redirect went outside `/embed/` to a non-auth route, rewrite it back

### Key Code Paths

```
Navigation to /application/o/authorize/
  └─> handleRequest()
      └─> isBloudRoute() returns true
      └─> unregisterClient()
      └─> passthrough (no rewriting)

Navigation to /openid/callback
  └─> handleRequest()
      └─> getRequestAction() uses activeAppContext
      └─> shouldRedirectOAuthCallback() returns true
      └─> Response.redirect() to /embed/{app}/openid/callback

Navigation to /embed/{app}/openid/callback
  └─> handleRequest()
      └─> handleEmbedNavigationRequest()
      └─> fetch() with redirect: 'follow'
      └─> App redirects to /openid-cb?token=...
      └─> Detect redirect outside /embed/
      └─> Response.redirect() to /embed/{app}/openid-cb
```

## Critical Finding: Set-Cookie Headers Lost During SW Redirect Following

### The Problem

When the Service Worker intercepts `/embed/{app}/openid/callback` and uses `fetch()` with `redirect: 'follow'`, the OAuth callback redirect chain is followed **internally by the SW**, not by the browser.

The redirect chain looks like:
1. SW fetches `/embed/{app}/openid/callback` → App responds with redirect to `/openid-cb?token=...` and **Set-Cookie header**
2. SW follows redirect internally → Gets final response
3. SW returns final response to browser

**Critical Issue**: The `Set-Cookie` header from step 1 (the intermediate redirect response) is **NOT applied to the browser's cookie jar**. This is by design per the Fetch specification - only the browser's navigation handling applies Set-Cookie headers, not `fetch()` responses (even when the SW returns them).

### Evidence

From the [Fetch Standard](https://fetch.spec.whatwg.org/#http-network-or-cache-fetch):
> If response's header list contains `Set-Cookie`, the user agent should ignore it for security reasons.

And from [MDN Service Worker Cookbook](https://serviceworke.rs/):
> Service workers cannot set cookies via `Set-Cookie` headers in responses.

The browser only processes Set-Cookie headers when:
1. It handles a navigation directly (no SW interception)
2. It receives a response from `fetch()` made by page JavaScript (not SW)

### Why Current Implementation Fails

Current flow:
```
Browser navigates to /embed/{app}/openid/callback
  └─> SW intercepts
      └─> SW fetches with redirect: 'follow'
      └─> App responds: 302 to /openid-cb + Set-Cookie: auth_token=...
      └─> SW follows redirect internally (cookie NOT applied)
      └─> SW gets final response
      └─> SW rewrites to /embed/{app}/openid-cb
      └─> Returns response to browser
  └─> Browser shows page (no cookie was set!)
```

### The Fix: Let OAuth Callbacks Bypass Service Worker

The solution is to **not intercept OAuth callback URLs**. By returning early without calling `event.respondWith()`, the browser handles the entire redirect chain natively.

**Two paths must bypass the SW:**

1. `/openid/callback` - Server exchanges auth code, sets session token, redirects to `/openid-cb`
   - Must bypass so Set-Cookie headers are applied by the browser

2. `/openid-cb` - Frontend processes token from URL, stores in IndexedDB, redirects to app
   - Must bypass because rewriting to `/embed/{app}/openid-cb` breaks the app router
   - The app expects the root-level path and may loop or fail to complete login

```typescript
// In handleRequest(), add early return for OAuth callbacks
if (url.pathname.includes('/openid/callback') || url.pathname === '/openid-cb') {
  console.log('[embed-sw] Letting OAuth callback bypass SW');
  return; // Don't call event.respondWith()
}
```

### Trade-off: Iframe URL After OAuth

When we bypass the SW for OAuth callbacks, the iframe will end up at the root-level URL (e.g., `/openid-cb?token=...`) instead of the embed namespace. This means:

1. **Cookies are properly set** ✓
2. **User is authenticated** ✓
3. **Iframe URL is at root level** - BUT this is temporary

The app will redirect to `/` or similar after processing the token. Since `activeAppContext` is still set, the SW will rewrite subsequent navigations back to `/embed/{app}/`.

### Root Cause Analysis

The original implementation used `activeAppContext` (set via postMessage) to determine which app to rewrite requests for. This works well for assets and non-navigation requests, but breaks OAuth callbacks:

- **Working version (pre-TypeScript rewrite)**: Only used `lastKnownAppContext` for non-navigate requests
- **Broken version**: Used `activeAppContext` for ALL requests including navigations

When `/openid-cb` was rewritten to `/embed/{app}/openid-cb`, the app router saw an unexpected path and either failed to process the token or entered a redirect loop.

## Testing

### Manual Test Flow

1. Navigate to `/apps/actual-budget`
2. Click "Login with SSO"
3. Authenticate with Authentik
4. Verify:
   - No 404 errors for static assets
   - User is logged in (not seeing "Sign in with SSO" again)
   - App functions correctly after login

### Expected Console Logs (After Fix)

```
[embed-sw] Client navigating to reserved route, unregistering: {clientApp: 'actual-budget', destination: '/application/o/authorize/'}
[embed-sw] Letting OAuth callback bypass SW for cookie handling
```

### Previous Expected Logs (Before Fix - Cookies Lost)

```
[embed-sw] Client navigating to reserved route, unregistering: {clientApp: 'actual-budget', destination: '/application/o/authorize/'}
[embed-sw] handleEmbedNavigationRequest: {fetchUrl: '.../embed/actual-budget/openid/callback...', appName: 'actual-budget'}
[embed-sw] Embed response: {status: 200, redirected: true, url: 'http://localhost:8081/openid-cb?token=...'}
[embed-sw] Rewriting redirect: {from: '.../openid-cb?token=...', to: '.../embed/actual-budget/openid-cb?token=...'}
```

## Related Files

- `services/host-agent/web/src/service-worker/handlers.ts` - Main request handling
- `services/host-agent/web/src/service-worker/core.ts` - Pure logic functions
- `services/host-agent/web/src/service-worker/types.ts` - Constants and types
- `apps/actual-budget/metadata.yaml` - App configuration including absolutePaths
- `apps/actual-budget/module.nix` - Environment variable configuration

## References

- [Fetch Standard](https://fetch.spec.whatwg.org/) - Explains opaqueredirect behavior
- [WHATWG fetch issue #1145](https://github.com/whatwg/fetch/issues/1145) - Discussion on opaqueredirect for same-origin
- [Service Worker Client API](https://developer.mozilla.org/en-US/docs/Web/API/FetchEvent/clientId) - clientId behavior during navigation
