// Bootstrap - waits for service worker to be controlling the page

import type { ProtectedEntry } from '../service-worker/types';

let ready = false;
let readyPromise: Promise<void> | null = null;

export function bootstrap(): Promise<void> {
  if (readyPromise) return readyPromise;

  readyPromise = (async () => {
    if (!('serviceWorker' in navigator)) {
      ready = true;
      return;
    }

    await navigator.serviceWorker.register('/service-worker.js', { scope: '/', type: 'module' });
    await navigator.serviceWorker.ready;
    ready = true;
  })();

  return readyPromise;
}

export function isReady(): boolean {
  return ready;
}

/**
 * Send the active app name to the service worker.
 * This is the single source of truth for which app is currently open.
 * Waits for the SW to be ready before sending.
 *
 * @param appName - The active app name, or null to clear
 */
export async function setActiveApp(appName: string | null): Promise<void> {
  if (!('serviceWorker' in navigator)) {
    return;
  }

  await bootstrap();

  if (navigator.serviceWorker.controller) {
    navigator.serviceWorker.controller.postMessage({
      type: 'SET_ACTIVE_APP',
      appName,
    });
  }
}

/**
 * Send protected IndexedDB entries to the service worker.
 * These entries will be injected into app HTML to intercept reads.
 * Uses MessageChannel to wait for SW acknowledgment before resolving.
 *
 * @param appName - The app these entries belong to
 * @param entries - Protected entries with database, store, key, and value
 */
export async function setProtectedEntries(
  appName: string,
  entries: ProtectedEntry[]
): Promise<void> {
  if (!('serviceWorker' in navigator)) {
    return;
  }

  await bootstrap();

  const controller = navigator.serviceWorker.controller;
  if (!controller) {
    return;
  }

  // Use MessageChannel to wait for SW acknowledgment
  return new Promise<void>((resolve) => {
    const channel = new MessageChannel();

    channel.port1.onmessage = () => {
      resolve();
    };

    controller.postMessage(
      {
        type: 'SET_PROTECTED_ENTRIES',
        appName,
        entries,
      },
      [channel.port2]
    );
  });
}
