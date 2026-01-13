/**
 * SSE Connection Manager - Handles Server-Sent Events for real-time updates
 */

import type { App } from '$lib/types';

export interface SSECallbacks {
	onApps: (apps: App[]) => void;
	onError?: (error: Event) => void;
	onOpen?: () => void;
}

let eventSource: EventSource | null = null;
let reconnectTimeout: ReturnType<typeof setTimeout> | null = null;

/**
 * Connect to the SSE endpoint for real-time app state updates
 */
export function connectSSE(callbacks: SSECallbacks): void {
	// Clean up any existing connection
	disconnectSSE();

	eventSource = new EventSource('/api/apps/events');

	eventSource.onmessage = (e) => {
		try {
			const apps: App[] = JSON.parse(e.data);
			callbacks.onApps(apps);
		} catch (err) {
			console.error('Failed to parse SSE data:', err);
		}
	};

	eventSource.onerror = (e) => {
		console.warn('SSE connection error, reconnecting...');
		callbacks.onError?.(e);

		eventSource?.close();
		eventSource = null;

		// Reconnect after a delay
		if (reconnectTimeout) clearTimeout(reconnectTimeout);
		reconnectTimeout = setTimeout(() => connectSSE(callbacks), 3000);
	};

	eventSource.onopen = () => {
		console.log('SSE connected for real-time updates');
		callbacks.onOpen?.();
	};
}

/**
 * Disconnect from the SSE endpoint
 */
export function disconnectSSE(): void {
	if (reconnectTimeout) {
		clearTimeout(reconnectTimeout);
		reconnectTimeout = null;
	}
	if (eventSource) {
		eventSource.close();
		eventSource = null;
	}
}

/**
 * Check if SSE is currently connected
 */
export function isSSEConnected(): boolean {
	return eventSource !== null && eventSource.readyState === EventSource.OPEN;
}
