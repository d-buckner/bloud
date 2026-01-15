// Service Worker Entry Point
// Registers event listeners and delegates to handlers

/// <reference lib="webworker" />

import { handleRequest, setActiveApp, setInterceptConfig } from './handlers';
import { MessageType } from './types';
import type { InterceptConfig } from './inject';

declare const self: ServiceWorkerGlobalScope;

self.addEventListener('install', (event) => {
  event.waitUntil(self.skipWaiting());
});

self.addEventListener('activate', (event) => {
  event.waitUntil(self.clients.claim());
});

// Listen for messages from the main frame
self.addEventListener('message', (event) => {
  if (!event.data || !event.data.type) {
    return;
  }

  switch (event.data.type) {
    case MessageType.SET_ACTIVE_APP: {
      const appName = event.data.appName;
      console.log('[embed-sw] Active app:', appName ?? '(none)');
      setActiveApp(appName);

      // Reply on the MessageChannel port to acknowledge processing
      if (event.ports && event.ports[0]) {
        event.ports[0].postMessage({ ack: true });
      }
      break;
    }
    case MessageType.SET_INTERCEPTS: {
      const config = event.data.config as InterceptConfig | null;
      if (config) {
        const idbCount = config.indexedDB?.intercepts.length ?? 0;
        const lsCount = config.localStorage?.intercepts.length ?? 0;
        console.log('[embed-sw] Intercepts set: IndexedDB=' + idbCount + ', localStorage=' + lsCount);
      }
      setInterceptConfig(config);

      // Reply on the MessageChannel port to acknowledge processing
      if (event.ports && event.ports[0]) {
        event.ports[0].postMessage({ ack: true });
      }
      break;
    }
  }
});

// Wrap handleRequest to catch any errors
function safeHandleRequest(event: FetchEvent): void {
  try {
    handleRequest(event);
  } catch (error) {
    console.error('[embed-sw] Error in handleRequest:', error);
    event.respondWith(fetch(event.request));
  }
}

self.addEventListener('fetch', safeHandleRequest);
