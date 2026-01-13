/**
 * Service Worker Bootstrap Service
 *
 * Manages service worker registration and communication.
 * Exposes a readable store for SW ready state.
 */

import { readable, get } from 'svelte/store';
import type { ProtectedEntry } from '../../service-worker/types';

// --- Service Worker Ready State ---

interface BootstrapState {
	ready: boolean;
	error: string | null;
}

let resolveReady: (() => void) | null = null;
const readyPromise = new Promise<void>((resolve) => {
	resolveReady = resolve;
});

/**
 * Service worker ready state as a Svelte store
 * Components can subscribe to this to show loading states
 */
export const swState = readable<BootstrapState>({ ready: false, error: null }, (set) => {
	// Bootstrap on first subscription
	bootstrapInternal(set);
	return () => {
		// No cleanup needed
	};
});

async function bootstrapInternal(set: (value: BootstrapState) => void): Promise<void> {
	if (!('serviceWorker' in navigator)) {
		set({ ready: true, error: null });
		resolveReady?.();
		return;
	}

	try {
		await navigator.serviceWorker.register('/service-worker.js', { scope: '/', type: 'module' });
		await navigator.serviceWorker.ready;
		set({ ready: true, error: null });
		resolveReady?.();
	} catch (err) {
		const errorMsg = err instanceof Error ? err.message : 'Failed to register service worker';
		set({ ready: false, error: errorMsg });
		console.error('Service worker registration failed:', err);
	}
}

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
	return get(swState).ready;
}

// --- Service Worker Communication ---

/**
 * Send the active app name to the service worker
 * This tells the SW which app context to use for URL rewriting
 */
export async function setActiveApp(appName: string | null): Promise<void> {
	if (!('serviceWorker' in navigator)) return;

	await waitForServiceWorker();

	if (navigator.serviceWorker.controller) {
		navigator.serviceWorker.controller.postMessage({
			type: 'SET_ACTIVE_APP',
			appName
		});
	}
}

/**
 * Send protected IndexedDB entries to the service worker
 * These entries will be injected into app HTML to intercept reads
 */
export async function setProtectedEntries(
	appName: string,
	entries: ProtectedEntry[]
): Promise<void> {
	if (!('serviceWorker' in navigator)) return;

	await waitForServiceWorker();

	const controller = navigator.serviceWorker.controller;
	if (!controller) return;

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
				entries
			},
			[channel.port2]
		);
	});
}
