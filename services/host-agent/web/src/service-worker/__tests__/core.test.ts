import { describe, it, expect, beforeEach } from 'vitest';
import {
  // Constants
  RequestAction,
  RequestType,
  PassthroughReason,
  RequestMode,
  RESERVED_SEGMENTS,
  // Types
  type ResponseLike,
  // Functions
  getAppFromPath,
  isBloudRoute,
  isServiceWorkerScript,
  shouldHandleRequest,
  rewriteRedirectLocation,
  rewriteRootUrl,
  isRedirectResponse,
  isAuthRedirect,
  shouldRedirectOAuthCallback,
  getRequestAction,
  processRedirectResponse,
  resetTestState,
  setActiveApp,
  getActiveApp,
  setInterceptConfig,
  getInterceptConfig,
  // Client ID tracking
  registerClient,
  getAppForClient,
  getAppFromReferer,
  getClientMapSize,
  appNeedsRewrite,
} from '../core';
import type { InterceptConfig } from '../inject';

/** Create a mock response with optional Location header */
function mockResponse(status: number, location?: string, type?: string): ResponseLike {
  const headers = new Map<string, string>();
  if (location) {
    headers.set('Location', location);
  }
  return {
    status,
    type,
    headers: {
      get: (name: string) => headers.get(name) ?? null,
    },
  };
}

describe('service-worker core', () => {
  describe('getAppFromPath', () => {
    it('extracts app name from /embed/{app}/ path', () => {
      expect(getAppFromPath('/embed/actual-budget/')).toBe('actual-budget');
      expect(getAppFromPath('/embed/adguard-home/')).toBe('adguard-home');
      expect(getAppFromPath('/embed/miniflux/')).toBe('miniflux');
    });

    it('extracts app name from /apps/{app}/ path', () => {
      expect(getAppFromPath('/apps/actual-budget/')).toBe('actual-budget');
      expect(getAppFromPath('/apps/adguard-home/')).toBe('adguard-home');
      expect(getAppFromPath('/apps/miniflux/')).toBe('miniflux');
    });

    it('extracts app name from /embed/{app} without trailing slash', () => {
      expect(getAppFromPath('/embed/actual-budget')).toBe('actual-budget');
    });

    it('extracts app name from /apps/{app} without trailing slash', () => {
      expect(getAppFromPath('/apps/actual-budget')).toBe('actual-budget');
    });

    it('extracts app name from deep paths', () => {
      expect(getAppFromPath('/embed/actual-budget/static/app.js')).toBe('actual-budget');
      expect(getAppFromPath('/embed/adguard-home/install.html')).toBe('adguard-home');
      expect(getAppFromPath('/apps/actual-budget/settings')).toBe('actual-budget');
      expect(getAppFromPath('/apps/adguard-home/control')).toBe('adguard-home');
    });

    it('returns null for non-app paths', () => {
      expect(getAppFromPath('/api/apps')).toBe(null);
      expect(getAppFromPath('/')).toBe(null);
      expect(getAppFromPath('/install.html')).toBe(null);
      expect(getAppFromPath('/catalog')).toBe(null);
    });

    it('returns null for /embed/ or /apps/ without app name', () => {
      expect(getAppFromPath('/embed/')).toBe(null);
      expect(getAppFromPath('/embed')).toBe(null);
      expect(getAppFromPath('/apps/')).toBe(null);
      expect(getAppFromPath('/apps')).toBe(null);
    });
  });

  describe('isBloudRoute', () => {
    it('returns true for API routes', () => {
      expect(isBloudRoute('/api/apps')).toBe(true);
      expect(isBloudRoute('/api/apps/installed')).toBe(true);
    });

    it('returns true for app management routes', () => {
      expect(isBloudRoute('/apps/')).toBe(true);
      expect(isBloudRoute('/catalog')).toBe(true);
      expect(isBloudRoute('/versions')).toBe(true);
      expect(isBloudRoute('/icons/app.png')).toBe(true);
    });

    it('returns true for SvelteKit routes', () => {
      expect(isBloudRoute('/_app/')).toBe(true);
      expect(isBloudRoute('/_app/immutable/chunks/app.js')).toBe(true);
    });

    it('returns true for Vite dev server routes', () => {
      expect(isBloudRoute('/@vite/client')).toBe(true);
      expect(isBloudRoute('/@fs/path')).toBe(true);
      expect(isBloudRoute('/node_modules/.vite/deps/chunk.js')).toBe(true);
      expect(isBloudRoute('/src/app.css')).toBe(true);
    });

    it('returns false for embed routes', () => {
      expect(isBloudRoute('/embed/actual-budget/')).toBe(false);
      expect(isBloudRoute('/embed/adguard-home/install.html')).toBe(false);
    });

    it('returns false for root-level app requests', () => {
      expect(isBloudRoute('/install.html')).toBe(false);
      expect(isBloudRoute('/static/app.js')).toBe(false);
      expect(isBloudRoute('/control/')).toBe(false);
    });

    // Note: Authentik routes are now on auth.localhost subdomain (cross-origin),
    // so they're passed through automatically via the cross-origin check.
    // No need to test them as Bloud routes.

    it('handles edge cases for first segment extraction', () => {
      // Single character paths
      expect(isBloudRoute('/a')).toBe(false);
    });

    it('returns true for root path (home page)', () => {
      expect(isBloudRoute('/')).toBe(true);
    });
  });

  describe('isServiceWorkerScript', () => {
    it('returns true for /sw.js', () => {
      expect(isServiceWorkerScript('/sw.js')).toBe(true);
    });

    it('returns true for /service-worker.js', () => {
      expect(isServiceWorkerScript('/service-worker.js')).toBe(true);
    });

    it('returns false for other js files', () => {
      expect(isServiceWorkerScript('/app.js')).toBe(false);
      expect(isServiceWorkerScript('/main.js')).toBe(false);
      expect(isServiceWorkerScript('/static/sw.js')).toBe(false);
    });

    it('returns false for similar but different paths', () => {
      expect(isServiceWorkerScript('/sw.json')).toBe(false);
      expect(isServiceWorkerScript('/sw.js.map')).toBe(false);
      expect(isServiceWorkerScript('/embed/app/sw.js')).toBe(false);
    });
  });

  describe('RESERVED_SEGMENTS', () => {
    it('contains all expected Bloud segments', () => {
      expect(RESERVED_SEGMENTS.has('api')).toBe(true);
      expect(RESERVED_SEGMENTS.has('apps')).toBe(true);
      expect(RESERVED_SEGMENTS.has('catalog')).toBe(true);
      expect(RESERVED_SEGMENTS.has('versions')).toBe(true);
      expect(RESERVED_SEGMENTS.has('icons')).toBe(true);
    });

    it('contains SvelteKit development segments', () => {
      expect(RESERVED_SEGMENTS.has('_app')).toBe(true);
      expect(RESERVED_SEGMENTS.has('node_modules')).toBe(true);
      expect(RESERVED_SEGMENTS.has('src')).toBe(true);
      expect(RESERVED_SEGMENTS.has('.svelte-kit')).toBe(true);
    });

    // Note: Authentik routes are now on auth.localhost subdomain (cross-origin),
    // so they don't need to be in RESERVED_SEGMENTS.

    it('does not contain app names', () => {
      expect(RESERVED_SEGMENTS.has('actual-budget')).toBe(false);
      expect(RESERVED_SEGMENTS.has('adguard-home')).toBe(false);
      expect(RESERVED_SEGMENTS.has('miniflux')).toBe(false);
    });
  });

  describe('shouldHandleRequest', () => {
    const origin = 'http://localhost:8080';

    it('rejects cross-origin requests', () => {
      const url = new URL('http://other.example.com/embed/actual-budget/');
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(false);
      expect(result.reason).toBe(PassthroughReason.CROSS_ORIGIN);
    });

    it('rejects Bloud routes', () => {
      const url = new URL('/api/apps', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(false);
      expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
    });

    it('handles embed requests for rewrite apps', () => {
      const url = new URL('/embed/actual-budget/', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(true);
      expect(result.type).toBe(RequestType.EMBED);
      expect(result.appName).toBe('actual-budget');
    });

    it('handles embed requests for adguard-home', () => {
      const url = new URL('/embed/adguard-home/', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(true);
      expect(result.type).toBe(RequestType.EMBED);
      expect(result.appName).toBe('adguard-home');
    });

    it('does not handle embed requests for apps that support BASE_URL', () => {
      // miniflux supports BASE_URL, so needsRewrite is false
      setActiveApp('miniflux', false);
      const url = new URL('/embed/miniflux/', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(false);
      expect(result.reason).toBe(PassthroughReason.EMBED_NO_REWRITE);
    });

    it('handles root-level requests', () => {
      const url = new URL('/install.html', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(true);
      expect(result.type).toBe(RequestType.ROOT);
    });

    it('handles root-level asset requests', () => {
      const url = new URL('/static/app.js', origin);
      const result = shouldHandleRequest(url, origin);
      expect(result.handle).toBe(true);
      expect(result.type).toBe(RequestType.ROOT);
    });
  });

  describe('rewriteRedirectLocation', () => {
    const origin = 'http://localhost:8080';

    it('rewrites absolute path redirects', () => {
      const result = rewriteRedirectLocation('/install.html', 'adguard-home', origin);
      expect(result).toBe('http://localhost:8080/embed/adguard-home/install.html');
    });

    it('rewrites redirects with query strings', () => {
      const result = rewriteRedirectLocation('/login?redirect=/dashboard', 'actual-budget', origin);
      expect(result).toBe('http://localhost:8080/embed/actual-budget/login?redirect=/dashboard');
    });

    it('rewrites full URL redirects to same origin', () => {
      const result = rewriteRedirectLocation('http://localhost:8080/control/', 'adguard-home', origin);
      expect(result).toBe('http://localhost:8080/embed/adguard-home/control/');
    });

    it('does not rewrite already-prefixed redirects', () => {
      const result = rewriteRedirectLocation('/embed/adguard-home/dashboard', 'adguard-home', origin);
      expect(result).toBe(null);
    });

    it('handles deep path redirects', () => {
      const result = rewriteRedirectLocation('/static/js/main.abc123.js', 'actual-budget', origin);
      expect(result).toBe('http://localhost:8080/embed/actual-budget/static/js/main.abc123.js');
    });
  });

  describe('rewriteRootUrl', () => {
    it('rewrites root path to embed path', () => {
      const url = new URL('http://localhost:8080/install.html');
      const result = rewriteRootUrl(url, 'adguard-home');
      expect(result).toBe('http://localhost:8080/embed/adguard-home/install.html');
    });

    it('preserves query strings', () => {
      const url = new URL('http://localhost:8080/page?foo=bar&baz=qux');
      const result = rewriteRootUrl(url, 'actual-budget');
      expect(result).toBe('http://localhost:8080/embed/actual-budget/page?foo=bar&baz=qux');
    });

    it('handles static asset paths', () => {
      const url = new URL('http://localhost:8080/install.214831cae43e25f9ac78.js');
      const result = rewriteRootUrl(url, 'adguard-home');
      expect(result).toBe('http://localhost:8080/embed/adguard-home/install.214831cae43e25f9ac78.js');
    });
  });

  describe('isRedirectResponse', () => {
    it('detects 301 redirects', () => {
      expect(isRedirectResponse(mockResponse(301))).toBe(true);
    });

    it('detects 302 redirects', () => {
      expect(isRedirectResponse(mockResponse(302))).toBe(true);
    });

    it('detects 307 redirects', () => {
      expect(isRedirectResponse(mockResponse(307))).toBe(true);
    });

    it('detects 308 redirects', () => {
      expect(isRedirectResponse(mockResponse(308))).toBe(true);
    });

    it('detects opaqueredirect type', () => {
      expect(isRedirectResponse(mockResponse(0, undefined, 'opaqueredirect'))).toBe(true);
    });

    it('does not flag 200 responses', () => {
      expect(isRedirectResponse(mockResponse(200))).toBe(false);
    });

    it('does not flag 404 responses', () => {
      expect(isRedirectResponse(mockResponse(404))).toBe(false);
    });

    it('does not flag 500 responses', () => {
      expect(isRedirectResponse(mockResponse(500))).toBe(false);
    });
  });

  describe('setActiveApp and getActiveApp', () => {
    beforeEach(() => {
      resetTestState();
    });

    it('sets and gets active app context', () => {
      expect(getActiveApp()).toBe(null);
      setActiveApp('actual-budget');
      expect(getActiveApp()).toBe('actual-budget');
    });

    it('clears active app with null', () => {
      setActiveApp('actual-budget');
      expect(getActiveApp()).toBe('actual-budget');
      setActiveApp(null);
      expect(getActiveApp()).toBe(null);
    });

    it('updates active app when switching', () => {
      setActiveApp('actual-budget');
      expect(getActiveApp()).toBe('actual-budget');
      setActiveApp('adguard-home');
      expect(getActiveApp()).toBe('adguard-home');
    });
  });

  describe('registerClient and getAppForClient', () => {
    beforeEach(() => {
      resetTestState();
    });

    it('starts with empty map', () => {
      expect(getClientMapSize()).toBe(0);
    });

    it('registers and retrieves client-app mapping', () => {
      registerClient('client-123', 'my-app');
      expect(getAppForClient('client-123')).toBe('my-app');
      expect(getClientMapSize()).toBe(1);
    });

    it('returns null for unregistered clientId', () => {
      expect(getAppForClient('unknown-client')).toBe(null);
    });

    it('returns null for undefined clientId', () => {
      expect(getAppForClient(undefined)).toBe(null);
    });

    it('can register multiple clients for different apps', () => {
      registerClient('client-1', 'app-a');
      registerClient('client-2', 'app-b');
      registerClient('client-3', 'app-a');

      expect(getAppForClient('client-1')).toBe('app-a');
      expect(getAppForClient('client-2')).toBe('app-b');
      expect(getAppForClient('client-3')).toBe('app-a');
      expect(getClientMapSize()).toBe(3);
    });

    it('overwrites existing registration for same clientId', () => {
      registerClient('client-123', 'app-a');
      expect(getAppForClient('client-123')).toBe('app-a');

      registerClient('client-123', 'app-b');
      expect(getAppForClient('client-123')).toBe('app-b');
      expect(getClientMapSize()).toBe(1);
    });

    it('is cleared by resetTestState', () => {
      registerClient('client-123', 'my-app');
      expect(getClientMapSize()).toBe(1);

      resetTestState();
      expect(getClientMapSize()).toBe(0);
      expect(getAppForClient('client-123')).toBe(null);
    });
  });

  describe('getAppFromReferer', () => {
    const origin = 'http://localhost:8080';

    it('extracts app from /embed/{app}/ referer', () => {
      expect(getAppFromReferer('http://localhost:8080/embed/my-app/', origin)).toBe('my-app');
      expect(getAppFromReferer('http://localhost:8080/embed/other-app/page.html', origin)).toBe('other-app');
    });

    it('extracts app from /apps/{app}/ referer', () => {
      expect(getAppFromReferer('http://localhost:8080/apps/my-app/', origin)).toBe('my-app');
    });

    it('returns null for non-app referer paths', () => {
      expect(getAppFromReferer('http://localhost:8080/', origin)).toBe(null);
      expect(getAppFromReferer('http://localhost:8080/api/apps', origin)).toBe(null);
      expect(getAppFromReferer('http://localhost:8080/catalog', origin)).toBe(null);
    });

    it('returns null for null referer', () => {
      expect(getAppFromReferer(null, origin)).toBe(null);
    });

    it('returns null for cross-origin referer', () => {
      expect(getAppFromReferer('http://other.example.com/embed/actual-budget/', origin)).toBe(null);
    });

    it('returns null for different port (cross-origin)', () => {
      expect(getAppFromReferer('http://localhost:3000/embed/actual-budget/', origin)).toBe(null);
    });

    it('returns null for invalid URL', () => {
      expect(getAppFromReferer('not-a-valid-url', origin)).toBe(null);
    });
  });

  describe('setInterceptConfig and getInterceptConfig', () => {
    beforeEach(() => {
      resetTestState();
    });

    it('starts with null config', () => {
      expect(getInterceptConfig()).toBe(null);
    });

    it('sets and gets intercept config with indexedDB', () => {
      const config: InterceptConfig = {
        indexedDB: {
          database: 'actual',
          intercepts: [{ store: 'asyncStorage', key: 'server-url', value: 'http://example.com' }],
        },
      };

      setInterceptConfig(config);
      expect(getInterceptConfig()).toEqual(config);
    });

    it('sets and gets intercept config with localStorage', () => {
      const config: InterceptConfig = {
        localStorage: {
          intercepts: [{ key: 'credentials', jsonPatch: { 'Servers.0.Address': 'http://test.com' } }],
        },
      };

      setInterceptConfig(config);
      expect(getInterceptConfig()).toEqual(config);
    });

    it('clears config with null', () => {
      const config: InterceptConfig = {
        indexedDB: {
          database: 'db',
          intercepts: [{ store: 's', key: 'k', value: 'v' }],
        },
      };

      setInterceptConfig(config);
      expect(getInterceptConfig()).toEqual(config);

      setInterceptConfig(null);
      expect(getInterceptConfig()).toBe(null);
    });

    it('updates config when switching apps', () => {
      const config1: InterceptConfig = {
        indexedDB: {
          database: 'db1',
          intercepts: [{ store: 's1', key: 'k1', value: 'v1' }],
        },
      };
      const config2: InterceptConfig = {
        indexedDB: {
          database: 'db2',
          intercepts: [{ store: 's2', key: 'k2', value: 'v2' }],
        },
      };

      setInterceptConfig(config1);
      expect(getInterceptConfig()?.indexedDB?.database).toBe('db1');

      setInterceptConfig(config2);
      expect(getInterceptConfig()?.indexedDB?.database).toBe('db2');
    });

    it('is cleared by resetTestState', () => {
      const config: InterceptConfig = {
        indexedDB: {
          database: 'db',
          intercepts: [{ store: 's', key: 'k', value: 'v' }],
        },
      };

      setInterceptConfig(config);
      expect(getInterceptConfig()).not.toBe(null);

      resetTestState();
      expect(getInterceptConfig()).toBe(null);
    });
  });

  describe('getRequestAction', () => {
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    it('returns passthrough for cross-origin requests', () => {
      const url = new URL('http://other.example.com/path');
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.CROSS_ORIGIN);
    });

    it('returns passthrough for Bloud routes', () => {
      const url = new URL('/api/apps', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
    });

    it('returns fetch for embed requests with rewrite app', () => {
      const url = new URL('/embed/adguard-home/', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/adguard-home/');
      expect(result.appName).toBe('adguard-home');
    });

    it('returns passthrough for embed requests with app that supports BASE_URL', () => {
      // miniflux supports BASE_URL, so needsRewrite is false
      setActiveApp('miniflux', false);
      const url = new URL('/embed/miniflux/', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.EMBED_NO_REWRITE);
    });

    it('returns fetch with rewritten URL when activeApp is set', () => {
      setActiveApp('adguard-home');
      const url = new URL('/install.html', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/adguard-home/install.html');
      expect(result.appName).toBe('adguard-home');
    });

    it('returns passthrough for root request without active app', () => {
      const url = new URL('/install.html', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.NO_APP_CONTEXT);
    });

    it('returns passthrough for root request with active app that supports BASE_URL', () => {
      // miniflux supports BASE_URL, so needsRewrite is false
      setActiveApp('miniflux', false);
      const url = new URL('/install.html', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.NO_APP_CONTEXT);
    });

    it('returns correct type for embed requests', () => {
      const url = new URL('/embed/actual-budget/', origin);
      const result = getRequestAction(url, origin);
      expect(result.type).toBe(RequestType.EMBED);
    });

    it('returns correct type for root requests with active app', () => {
      setActiveApp('adguard-home');
      const url = new URL('/install.html', origin);
      const result = getRequestAction(url, origin);
      expect(result.type).toBe(RequestType.ROOT);
    });
  });

  describe('processRedirectResponse', () => {
    const origin = 'http://localhost:8080';

    it('returns null for non-redirect response', () => {
      const result = processRedirectResponse(mockResponse(200), 'adguard-home', origin);
      expect(result).toBe(null);
    });

    it('returns null for redirect without Location header', () => {
      const result = processRedirectResponse(mockResponse(302), 'adguard-home', origin);
      expect(result).toBe(null);
    });

    it('rewrites redirect location for 302 response', () => {
      const result = processRedirectResponse(mockResponse(302, '/install.html'), 'adguard-home', origin);
      expect(result).toBe('http://localhost:8080/embed/adguard-home/install.html');
    });

    it('rewrites redirect location for 301 response', () => {
      const result = processRedirectResponse(mockResponse(301, '/dashboard'), 'actual-budget', origin);
      expect(result).toBe('http://localhost:8080/embed/actual-budget/dashboard');
    });

    it('returns null for already-prefixed redirect', () => {
      const result = processRedirectResponse(mockResponse(302, '/embed/adguard-home/install.html'), 'adguard-home', origin);
      expect(result).toBe(null);
    });

    it('handles opaqueredirect type', () => {
      const result = processRedirectResponse(mockResponse(0, '/install.html', 'opaqueredirect'), 'adguard-home', origin);
      expect(result).toBe('http://localhost:8080/embed/adguard-home/install.html');
    });
  });

  describe('Real-world scenarios with active app', () => {
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    describe('AdGuard Home initial load', () => {
      it('step 1: user navigates to /embed/adguard-home/', () => {
        const url = new URL('/embed/adguard-home/', origin);
        const result = shouldHandleRequest(url, origin);
        expect(result.handle).toBe(true);
        expect(result.type).toBe(RequestType.EMBED);
        expect(result.appName).toBe('adguard-home');
      });

      it('step 2: server redirects to /install.html - rewrite to /embed/adguard-home/install.html', () => {
        const redirectLocation = '/install.html';
        const newLocation = rewriteRedirectLocation(redirectLocation, 'adguard-home', origin);
        expect(newLocation).toBe('http://localhost:8080/embed/adguard-home/install.html');
      });

      it('step 3: install.html requests /install.js with active app set', () => {
        // Main frame sets active app via postMessage
        setActiveApp('adguard-home');

        const url = new URL('/install.214831cae43e25f9ac78.js', origin);
        const result = getRequestAction(url, origin);
        expect(result.action).toBe(RequestAction.FETCH);
        expect(result.fetchUrl).toBe('http://localhost:8080/embed/adguard-home/install.214831cae43e25f9ac78.js');
        expect(result.appName).toBe('adguard-home');
      });
    });

    describe('Actual Budget with WebAssembly', () => {
      it('handles WASM file requests with active app', () => {
        setActiveApp('actual-budget');

        const url = new URL('/static/wasm/app.wasm', origin);
        const result = getRequestAction(url, origin);
        expect(result.action).toBe(RequestAction.FETCH);
        expect(result.fetchUrl).toBe('http://localhost:8080/embed/actual-budget/static/wasm/app.wasm');
      });
    });

    describe('Miniflux (supports BASE_URL, no rewriting needed)', () => {
      it('does not intercept miniflux embed requests', () => {
        // miniflux supports BASE_URL, so needsRewrite is false
        setActiveApp('miniflux', false);
        const url = new URL('/embed/miniflux/', origin);
        const result = shouldHandleRequest(url, origin);
        expect(result.handle).toBe(false);
        expect(result.reason).toBe(PassthroughReason.EMBED_NO_REWRITE);
      });
    });

    describe('Edge cases', () => {
      it('handles root request without active app (no app context)', () => {
        const url = new URL('/install.html', origin);
        const result = getRequestAction(url, origin);
        expect(result.action).toBe(RequestAction.PASSTHROUGH);
        expect(result.reason).toBe(PassthroughReason.NO_APP_CONTEXT);
      });

      it('does not rewrite Bloud API calls even with active app', () => {
        setActiveApp('my-app');
        const url = new URL('/api/something', origin);
        const result = getRequestAction(url, origin);
        expect(result.action).toBe(RequestAction.PASSTHROUGH);
        expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
      });
    });

    describe('Reserved path conflict resolution via clientId', () => {
      // NOTE: The core getRequestAction function returns PASSTHROUGH for reserved paths
      // like /api/ because they're in RESERVED_SEGMENTS.
      // However, handlers.ts uses the clientId map to bypass this:
      // If a request comes from a registered app iframe (via clientId),
      // the handler rewrites /api/... to /embed/{app}/api/... BEFORE calling
      // getRequestAction, allowing the embedded app's API to work.

      it('core logic treats /api as Bloud route (passthrough)', () => {
        setActiveApp('my-app');
        const url = new URL('/api/v3/status', origin);
        const result = getRequestAction(url, origin);
        expect(result.action).toBe(RequestAction.PASSTHROUGH);
        expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
      });

      it('clientId can be used to identify app for reserved paths', () => {
        // handlers.ts checks clientId BEFORE calling getRequestAction
        // to handle this case - we just verify the clientId mechanism works
        registerClient('iframe-abc', 'my-app');
        expect(getAppForClient('iframe-abc')).toBe('my-app');
      });

      it('clientId bypass works for Radarr/Sonarr /api/v3/* paths (regression test)', () => {
        // This test verifies the fix for the bug where /api/v3/* requests from
        // embedded apps like Radarr/Sonarr were incorrectly treated as Bloud routes
        // and returned 404 instead of being rewritten.
        //
        // The handlers.ts logic for clientId-based rewriting is:
        //   const clientApp = getAppForClient(event.clientId);
        //   if (clientApp && appNeedsRewrite(clientApp) && url.origin === origin) {
        //     if (!url.pathname.startsWith(EMBED_PATH_PREFIX)) {
        //       const fetchUrl = rewriteRootUrl(url, clientApp);
        //       // ... handle request with rewritten URL
        //     }
        //   }
        //
        // CRITICAL: The clientId check must NOT include isBloudRoute() because
        // the clientId is authoritative - if we registered the iframe as belonging
        // to radarr, then ALL requests from that clientId should be rewritten.

        // Simulate: Radarr iframe was registered with clientId
        registerClient('radarr-iframe-123', 'radarr');

        // Simulate: Request comes from that clientId to /api/v3/movies
        const clientApp = getAppForClient('radarr-iframe-123');
        expect(clientApp).toBe('radarr');

        // Verify app needs rewriting (Radarr doesn't support BASE_URL)
        expect(appNeedsRewrite(clientApp!)).toBe(true);

        // Simulate the handler's rewrite logic
        const url = new URL('/api/v3/movie', origin);
        const isNotAlreadyEmbed = !url.pathname.startsWith('/embed/');
        expect(isNotAlreadyEmbed).toBe(true);

        // The handler should rewrite this to /embed/radarr/api/v3/movie
        // (NOT pass it through as a "Bloud route")
        const fetchUrl = rewriteRootUrl(url, clientApp!);
        expect(fetchUrl).toBe('http://localhost:8080/embed/radarr/api/v3/movie');
      });

      it('clientId bypass handles all RESERVED_SEGMENTS paths from embedded apps', () => {
        // Embedded apps might request paths that match any reserved segment.
        // When we have a valid clientId, we should rewrite ALL of them.
        const reservedPaths = [
          '/api/v3/system/status', // Radarr/Sonarr API
          '/api/v1/config', // Generic API pattern
          '/icons/app-icon.png', // App might serve its own icons
          '/images/poster.jpg', // App might serve images
        ];

        registerClient('test-app-iframe', 'test-app');
        const clientApp = getAppForClient('test-app-iframe');

        for (const path of reservedPaths) {
          const url = new URL(path, origin);
          const fetchUrl = rewriteRootUrl(url, clientApp!);
          expect(fetchUrl).toBe(`http://localhost:8080/embed/test-app${path}`);
        }
      });
    });
  });

  describe('App switching scenario', () => {
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    it('correctly switches context between apps', () => {
      // Start with AdGuard
      setActiveApp('adguard-home');
      const adguardAsset = new URL('/control.js', origin);
      const adguardResult = getRequestAction(adguardAsset, origin);
      expect(adguardResult.appName).toBe('adguard-home');

      // User switches to Actual Budget
      setActiveApp('actual-budget');
      const actualAsset = new URL('/static/app.js', origin);
      const actualResult = getRequestAction(actualAsset, origin);
      expect(actualResult.appName).toBe('actual-budget');
    });

    it('clears context when navigating away', () => {
      setActiveApp('adguard-home');
      expect(getActiveApp()).toBe('adguard-home');

      // User navigates away, main frame clears active app
      setActiveApp(null);
      expect(getActiveApp()).toBe(null);

      // Root requests no longer rewrite
      const url = new URL('/install.html', origin);
      const result = getRequestAction(url, origin);
      expect(result.action).toBe(RequestAction.PASSTHROUGH);
    });
  });

  describe('Regression tests: fetchUrl construction', () => {
    // These tests verify the full rewrite flow - activeApp set -> fetchUrl correctly constructed
    // This catches regressions where getRequestAction logic is broken but unit tests pass
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    it('rewrites root request to embed path when activeApp is set', () => {
      setActiveApp('actual-budget');
      const url = new URL('/static/app.js', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/actual-budget/static/app.js');
      expect(result.type).toBe(RequestType.ROOT);
    });

    it('preserves query string in rewritten URL', () => {
      setActiveApp('actual-budget');
      const url = new URL('/sync?token=abc&ts=123', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/actual-budget/sync?token=abc&ts=123');
    });

    it('returns passthrough for app that supports BASE_URL (needsRewrite: false)', () => {
      // miniflux supports BASE_URL so it doesn't need rewriting
      setActiveApp('miniflux', false);
      const url = new URL('/entries', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.NO_APP_CONTEXT);
    });

    it('handles deeply nested paths', () => {
      setActiveApp('actual-budget');
      const url = new URL('/static/js/chunks/vendor.abc123.js', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/actual-budget/static/js/chunks/vendor.abc123.js');
    });
  });

  describe('Regression tests: reserved segment conflicts', () => {
    // These tests document the expected behavior when embedded apps use paths
    // that conflict with RESERVED_SEGMENTS (like /api, /icons, etc.)
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    it('core logic passes through /api even with activeApp set', () => {
      // This is expected - handlers.ts uses clientId to bypass this
      setActiveApp('sonarr');
      const url = new URL('/api/v3/system/status', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
    });

    it('core logic passes through /icons even with activeApp set', () => {
      setActiveApp('some-app');
      const url = new URL('/icons/favicon.png', origin);
      const result = getRequestAction(url, origin);

      expect(result.action).toBe(RequestAction.PASSTHROUGH);
      expect(result.reason).toBe(PassthroughReason.BLOUD_ROUTE);
    });

    it('clientId + rewriteRootUrl can bypass reserved segments', () => {
      // This tests the mechanism handlers.ts uses to bypass reserved segments
      // When clientId is known, handlers.ts calls rewriteRootUrl directly
      registerClient('sonarr-iframe', 'sonarr');
      const appName = getAppForClient('sonarr-iframe');

      expect(appName).toBe('sonarr');

      // Verify rewriteRootUrl produces correct URL for /api path
      const url = new URL('http://localhost:8080/api/v3/status');
      const rewrittenUrl = rewriteRootUrl(url, appName!);
      expect(rewrittenUrl).toBe('http://localhost:8080/embed/sonarr/api/v3/status');
    });

    it('Referer fallback can identify app for requests without activeApp', () => {
      // When activeApp is null and clientId is unknown, Referer is the fallback
      const referer = 'http://localhost:8080/embed/radarr/movies';
      const appName = getAppFromReferer(referer, origin);

      expect(appName).toBe('radarr');

      // Verify rewriteRootUrl works with the extracted app
      const url = new URL('http://localhost:8080/api/v3/movie/123');
      const rewrittenUrl = rewriteRootUrl(url, appName!);
      expect(rewrittenUrl).toBe('http://localhost:8080/embed/radarr/api/v3/movie/123');
    });
  });

  describe('URL edge cases', () => {
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    describe('rewriteRedirectLocation edge cases', () => {
      it('handles root path redirect', () => {
        const result = rewriteRedirectLocation('/', 'adguard-home', origin);
        expect(result).toBe('http://localhost:8080/embed/adguard-home/');
      });

      it('handles redirect with hash fragment', () => {
        const result = rewriteRedirectLocation('/page#section', 'actual-budget', origin);
        expect(result).toBe('http://localhost:8080/embed/actual-budget/page');
      });

      it('handles complex query strings', () => {
        const result = rewriteRedirectLocation('/login?redirect=%2Fdashboard&foo=bar%20baz', 'actual-budget', origin);
        expect(result).toBe('http://localhost:8080/embed/actual-budget/login?redirect=%2Fdashboard&foo=bar%20baz');
      });

      it('handles double slashes in path', () => {
        const result = rewriteRedirectLocation('//path//to//page', 'adguard-home', origin);
        expect(result).toContain('/embed/adguard-home/');
      });
    });

    describe('rewriteRootUrl edge cases', () => {
      it('handles URL with port in origin', () => {
        const url = new URL('http://localhost:3000/install.html');
        const result = rewriteRootUrl(url, 'adguard-home');
        expect(result).toBe('http://localhost:3000/embed/adguard-home/install.html');
      });

      it('handles HTTPS URLs', () => {
        const url = new URL('https://example.com/page');
        const result = rewriteRootUrl(url, 'actual-budget');
        expect(result).toBe('https://example.com/embed/actual-budget/page');
      });

      it('handles root path', () => {
        const url = new URL('http://localhost:8080/');
        const result = rewriteRootUrl(url, 'adguard-home');
        expect(result).toBe('http://localhost:8080/embed/adguard-home/');
      });
    });

    describe('getAppFromPath edge cases', () => {
      it('handles paths with special characters', () => {
        expect(getAppFromPath('/embed/my-app-123/')).toBe('my-app-123');
        expect(getAppFromPath('/apps/app_name/')).toBe('app_name');
      });

      it('handles very long app names', () => {
        const longName = 'a'.repeat(100);
        expect(getAppFromPath(`/embed/${longName}/`)).toBe(longName);
      });

      it('handles paths with query strings', () => {
        expect(getAppFromPath('/embed/app?query=1')).toBe('app?query=1');
      });
    });

    describe('shouldHandleRequest edge cases', () => {
      it('handles URLs with hash fragments', () => {
        const url = new URL('/embed/actual-budget/#section', origin);
        const result = shouldHandleRequest(url, origin);
        expect(result.handle).toBe(true);
        expect(result.appName).toBe('actual-budget');
      });

      it('handles deep nested paths', () => {
        const url = new URL('/embed/actual-budget/a/b/c/d/e/file.js', origin);
        const result = shouldHandleRequest(url, origin);
        expect(result.handle).toBe(true);
        expect(result.appName).toBe('actual-budget');
      });

      it('handles different port for same host (cross-origin)', () => {
        const url = new URL('http://localhost:3000/embed/actual-budget/');
        const result = shouldHandleRequest(url, origin);
        expect(result.handle).toBe(false);
        expect(result.reason).toBe(PassthroughReason.CROSS_ORIGIN);
      });
    });
  });

  describe('isRedirectResponse edge cases', () => {
    it('detects 303 redirects', () => {
      expect(isRedirectResponse(mockResponse(303))).toBe(true);
    });

    it('handles boundary cases', () => {
      expect(isRedirectResponse(mockResponse(299))).toBe(false);
      expect(isRedirectResponse(mockResponse(300))).toBe(true);
      expect(isRedirectResponse(mockResponse(399))).toBe(true);
      expect(isRedirectResponse(mockResponse(400))).toBe(false);
    });

    it('handles responses with both type and status', () => {
      expect(isRedirectResponse(mockResponse(200, undefined, 'opaqueredirect'))).toBe(true);
      expect(isRedirectResponse(mockResponse(302, undefined, 'basic'))).toBe(true);
    });
  });

  describe('isAuthRedirect', () => {
    const origin = 'http://localhost:8080';

    describe('detects auth subdomain redirects', () => {
      it('returns true for auth.localhost', () => {
        expect(isAuthRedirect('http://auth.localhost:8080/login', origin)).toBe(true);
      });

      it('returns true for auth.localhost with path', () => {
        expect(isAuthRedirect('http://auth.localhost:8080/application/o/authorize/', origin)).toBe(true);
      });

      it('returns true for auth.example.com', () => {
        expect(isAuthRedirect('http://auth.example.com/login', origin)).toBe(true);
      });

      it('returns true for auth subdomain with different port', () => {
        expect(isAuthRedirect('http://auth.localhost:9000/login', origin)).toBe(true);
      });

      it('returns true for https auth subdomain', () => {
        expect(isAuthRedirect('https://auth.example.com/login', origin)).toBe(true);
      });
    });

    describe('returns false for non-auth redirects', () => {
      it('returns false for same-origin redirect', () => {
        expect(isAuthRedirect('/login', origin)).toBe(false);
      });

      it('returns false for localhost without auth prefix', () => {
        expect(isAuthRedirect('http://localhost:8080/login', origin)).toBe(false);
      });

      it('returns false for other subdomains', () => {
        expect(isAuthRedirect('http://api.localhost:8080/login', origin)).toBe(false);
        expect(isAuthRedirect('http://app.localhost:8080/login', origin)).toBe(false);
      });

      it('returns false for domain containing auth but not as subdomain', () => {
        expect(isAuthRedirect('http://authentication.com/login', origin)).toBe(false);
        expect(isAuthRedirect('http://myauth.com/login', origin)).toBe(false);
      });

      it('returns false for relative paths', () => {
        expect(isAuthRedirect('/embed/app/login', origin)).toBe(false);
        expect(isAuthRedirect('/auth/login', origin)).toBe(false);
      });
    });

    describe('handles edge cases', () => {
      it('returns false for invalid URL', () => {
        expect(isAuthRedirect('not-a-url', origin)).toBe(false);
      });

      it('returns false for empty string', () => {
        expect(isAuthRedirect('', origin)).toBe(false);
      });

      it('handles URL with query params', () => {
        expect(isAuthRedirect('http://auth.localhost:8080/login?next=/app', origin)).toBe(true);
      });

      it('handles URL with fragment', () => {
        expect(isAuthRedirect('http://auth.localhost:8080/login#section', origin)).toBe(true);
      });
    });
  });

  describe('shouldRedirectOAuthCallback', () => {
    describe('returns true for OAuth callback navigation requests', () => {
      it('returns true for navigate mode with /openid/callback', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/openid/callback')).toBe(true);
      });

      it('returns true for navigate mode with /openid/callback with query params', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/openid/callback?code=abc&state=xyz')).toBe(true);
      });

      it('returns true for /embed/app/openid/callback', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/embed/actual-budget/openid/callback')).toBe(true);
      });
    });

    describe('returns false for non-OAuth or non-navigation requests', () => {
      it('returns false for cors mode even with /openid/callback', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.CORS, '/openid/callback')).toBe(false);
      });

      it('returns false for same-origin mode even with /openid/callback', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.SAME_ORIGIN, '/openid/callback')).toBe(false);
      });

      it('returns false for navigate mode with other paths', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/login')).toBe(false);
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/callback')).toBe(false);
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/oauth/callback')).toBe(false);
      });

      it('returns false for navigate mode with openid in different context', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/openid')).toBe(false);
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/openid-config')).toBe(false);
      });
    });

    describe('edge cases', () => {
      it('returns false for undefined mode', () => {
        expect(shouldRedirectOAuthCallback(undefined, '/openid/callback')).toBe(false);
      });

      it('returns false for null mode', () => {
        expect(shouldRedirectOAuthCallback(null, '/openid/callback')).toBe(false);
      });

      it('returns false for empty pathname', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '')).toBe(false);
      });

      it('handles case sensitivity (pathname is case-sensitive)', () => {
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/openid/CALLBACK')).toBe(false);
        expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, '/OPENID/callback')).toBe(false);
      });
    });
  });

  describe('OAuth callback integration scenario', () => {
    const origin = 'http://localhost:8080';

    beforeEach(() => {
      resetTestState();
    });

    it('OAuth callback from Authentik gets intercepted and should redirect', () => {
      // User completes SSO login, Authentik redirects to /openid/callback?code=...
      setActiveApp('actual-budget');

      const url = new URL('/openid/callback?code=abc123&state=xyz', origin);
      const result = getRequestAction(url, origin);

      // SW should intercept this as a root request
      expect(result.action).toBe(RequestAction.FETCH);
      expect(result.type).toBe(RequestType.ROOT);
      expect(result.fetchUrl).toBe('http://localhost:8080/embed/actual-budget/openid/callback?code=abc123&state=xyz');

      // And when it's a navigate request, shouldRedirectOAuthCallback returns true
      expect(shouldRedirectOAuthCallback(RequestMode.NAVIGATE, url.pathname)).toBe(true);
    });

    it('non-navigate callback requests should not redirect', () => {
      // AJAX/fetch requests to callback endpoint should use fetch-and-return
      setActiveApp('actual-budget');

      const url = new URL('/openid/callback', origin);

      // cors mode (from fetch API call)
      expect(shouldRedirectOAuthCallback(RequestMode.CORS, url.pathname)).toBe(false);
      // same-origin mode
      expect(shouldRedirectOAuthCallback(RequestMode.SAME_ORIGIN, url.pathname)).toBe(false);
    });
  });
});
