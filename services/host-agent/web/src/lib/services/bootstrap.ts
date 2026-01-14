/**
 * Service Worker Bootstrap Service
 *
 * Manages service worker registration and communication.
 * The SW is used for URL rewriting for apps that don't support BASE_URL.
 */

// --- Service Worker Ready State ---

let swReady = false;
let resolveReady: (() => void) | null = null;
const readyPromise = new Promise<void>((resolve) => {
	resolveReady = resolve;
});

/**
 * Initialize the service worker.
 * Called once at app startup.
 */
async function initServiceWorker(): Promise<void> {
	if (typeof window === 'undefined' || !('serviceWorker' in navigator)) {
		swReady = true;
		resolveReady?.();
		return;
	}

	try {
		await navigator.serviceWorker.register('/service-worker.js', { scope: '/', type: 'module' });
		await navigator.serviceWorker.ready;
		swReady = true;
		resolveReady?.();
	} catch (err) {
		console.error('Service worker registration failed:', err);
		// Still mark as ready so the app doesn't block forever
		swReady = true;
		resolveReady?.();
	}
}

// Start registration immediately when module loads
initServiceWorker();

/**
 * Wait for the service worker to be ready
 * Use this in async code that needs to wait for SW
 */
export function waitForServiceWorker(): Promise<void> {
	return readyPromise;
}

/**
 * Check if the service worker is ready (non-reactive)
 */
export function isServiceWorkerReady(): boolean {
	return swReady;
}

// --- Service Worker Communication ---

/**
 * Send the active app name to the service worker and wait for acknowledgment.
 * Uses MessageChannel for request-response pattern to ensure the SW has
 * processed the message before iframe content starts loading.
 */
export async function setActiveApp(appName: string | null): Promise<void> {
	if (typeof window === 'undefined' || !('serviceWorker' in navigator)) return;

	await waitForServiceWorker();

	const controller = navigator.serviceWorker.controller;
	if (!controller) return;

	return new Promise<void>((resolve) => {
		const channel = new MessageChannel();

		// Set up response handler on port1
		channel.port1.onmessage = () => {
			resolve();
		};

		// Send message with port2 for SW to reply on
		controller.postMessage(
			{
				type: 'SET_ACTIVE_APP',
				appName
			},
			[channel.port2]
		);

		// Fallback timeout in case SW doesn't respond (shouldn't happen)
		setTimeout(resolve, 100);
	});
}
