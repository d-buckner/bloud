// Bootstrap - waits for service worker to be controlling the page

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
