// Service Worker Entry Point
// Registers event listeners and delegates to handlers

/// <reference lib="webworker" />

import { handleRequest, setActiveApp, setInterceptConfig } from './handlers';
import { MessageType } from './types';
import type { IndexedDBInterceptConfig } from './inject';

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
    case MessageType.SET_INDEXEDDB_INTERCEPTS: {
      const config = event.data.config as IndexedDBInterceptConfig | null;
      if (config) {
        console.log('[embed-sw] IndexedDB intercepts set:', config.database, config.intercepts.length, 'entries');
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
