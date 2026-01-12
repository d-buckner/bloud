import { apps, loading, error } from './apps';
import type { App, InstallResult, UninstallResult } from '$lib/types';

let eventSource: EventSource | null = null;
let reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
let initialized = false;

/**
 * Initialize the app store by fetching initial data via GET,
 * then connecting to SSE for real-time updates.
 * Should be called once when the app starts (e.g., in +layout.svelte).
 */
export async function initApps(): Promise<void> {
	if (initialized) return;
	initialized = true;

	// Fetch initial data via GET - this is reliable and doesn't have race conditions
	try {
		const res = await fetch('/api/apps/installed');
		if (res.ok) {
			const appList: App[] = await res.json();
			apps.set(appList);
			loading.set(false);
			error.set(null);
		} else {
			const data = await res.json();
			error.set(data.error || 'Failed to load apps');
			loading.set(false);
		}
	} catch (err) {
		console.error('Failed to fetch initial apps:', err);
		error.set('Failed to connect to server');
		loading.set(false);
	}

	// Connect SSE for real-time updates (not initial data)
	connectSSE();
}

/**
 * Connect to the SSE endpoint for real-time app state updates.
 * Initial data is fetched via GET in initApps(), SSE is only for updates.
 */
function connectSSE(): void {
	eventSource = new EventSource('/api/apps/events');

	eventSource.onmessage = (e) => {
		try {
			const appList: App[] = JSON.parse(e.data);
			apps.set(appList);
			error.set(null);
		} catch (err) {
			console.error('Failed to parse SSE data:', err);
		}
	};

	eventSource.onerror = () => {
		console.warn('SSE connection error, reconnecting...');
		eventSource?.close();
		eventSource = null;

		// Reconnect after a delay
		if (reconnectTimeout) clearTimeout(reconnectTimeout);
		reconnectTimeout = setTimeout(connectSSE, 3000);
	};

	eventSource.onopen = () => {
		console.log('SSE connected for real-time updates');
	};
}

/**
 * Disconnect from the SSE endpoint.
 * Call this when cleaning up (e.g., in onDestroy).
 */
export function disconnectApps(): void {
	if (reconnectTimeout) {
		clearTimeout(reconnectTimeout);
		reconnectTimeout = null;
	}
	if (eventSource) {
		eventSource.close();
		eventSource = null;
	}
	initialized = false;
}

/**
 * Install an app with optional integration choices.
 * Returns the install result from the server.
 * The store will also be updated via SSE when the backend state changes.
 */
export async function installApp(
	name: string,
	choices: Record<string, string> = {}
): Promise<InstallResult> {
	const res = await fetch(`/api/apps/${name}/install`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ choices })
	});

	const result: InstallResult = await res.json();

	if (!res.ok) {
		throw new Error(result.error || 'Install failed');
	}

	return result;
}

/**
 * Uninstall an app.
 * Optimistically sets status to 'uninstalling' for immediate UI feedback.
 * Returns the uninstall result from the server.
 * The store will also be updated via SSE when the backend state changes.
 */
export async function uninstallApp(name: string): Promise<UninstallResult> {
	// Optimistic update: set status to 'uninstalling' immediately
	apps.update((current) =>
		current.map((app) =>
			app.name === name ? { ...app, status: 'uninstalling' as const } : app
		)
	);

	const res = await fetch(`/api/apps/${name}/uninstall`, {
		method: 'POST'
	});

	const result: UninstallResult = await res.json();

	if (!res.ok) {
		// Revert optimistic update on error
		apps.update((current) =>
			current.map((app) =>
				app.name === name ? { ...app, status: 'running' as const } : app
			)
		);
		throw new Error(result.error || 'Uninstall failed');
	}

	return result;
}
