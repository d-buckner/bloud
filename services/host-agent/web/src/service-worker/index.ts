// Service Worker Entry Point
// Registers event listeners and delegates to handlers

/// <reference lib="webworker" />

import { handleRequest, setActiveApp } from './handlers';
import { getRequestAction } from './core';

declare const self: ServiceWorkerGlobalScope;

console.log('[embed-sw] Service worker script loaded');

self.addEventListener('install', (event) => {
  console.log('[embed-sw] Installing...');
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (event) => {
  console.log('[embed-sw] Activating...');
  event.waitUntil(
    (async () => {
      await self.clients.claim();
      console.log('[embed-sw] Activated and claimed clients');
    })()
  );
});

// Listen for messages from the main frame to set active app context
self.addEventListener('message', (event) => {
  if (event.data && event.data.type === 'SET_ACTIVE_APP') {
    const appName = event.data.appName;
    console.log('[embed-sw] Setting active app:', appName);
    setActiveApp(appName);
  }
});

// Wrap handleRequest to catch any errors
function safeHandleRequest(event: FetchEvent): void {
  const url = new URL(event.request.url);
  const destination = event.request.destination;
  const origin = self.location.origin;

  // Get the action to understand decision flow
  const result = getRequestAction(url, origin);

  // Log with decision result
  console.log(
    '[embed-sw] FETCH:',
    url.pathname,
    'dest=' + destination,
    'action=' + result.action + (result.reason ? '(' + result.reason + ')' : ''),
    'type=' + (result.type || '-'),
    'app=' + (result.appName || '-')
  );

  // Extra logging for sw.js requests
  if (url.pathname.endsWith('sw.js')) {
    console.log(
      '[embed-sw] SW.JS request:',
      'url=' + url.href,
      'dest=' + destination,
      'mode=' + event.request.mode,
      'action=' + result.action,
      'fetchUrl=' + (result.fetchUrl || '-')
    );
  }

  // If this is a root script/style that should redirect, log the target URL
  if (
    result.action === 'fetch' &&
    result.type === 'root' &&
    (destination === 'script' || destination === 'style')
  ) {
    console.log('[embed-sw] -> REDIRECT to:', result.fetchUrl);
  }

  try {
    handleRequest(event);
  } catch (error) {
    console.error('[embed-sw] Error in handleRequest:', error);
    // On error, just pass through
    event.respondWith(fetch(event.request));
  }
}

self.addEventListener('fetch', safeHandleRequest);
