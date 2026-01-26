/**
 * App Lifecycle Service - Manages app installation and uninstallation
 *
 * This service orchestrates:
 * - Initial app data fetch
 * - SSE connection for real-time updates
 * - Install/uninstall operations with optimistic UI updates
 *
 * Business logic lives here, not in components.
 *
 * Note: Layout sync for apps is handled by the backend. The backend adds apps to
 * users' layouts on install and removes them on uninstall. The frontend just needs
 * to refresh the layout store when apps change to pick up backend changes.
 */

import { apps, loading, error } from '$lib/stores/apps';
import { layout } from '$lib/stores/layout';
import { fetchInstalledApps, installApp as apiInstall, uninstallApp as apiUninstall } from '$lib/api/apps';
import { connectSSE, disconnectSSE } from '$lib/api/sse';
import type { InstallResult, UninstallResult } from '$lib/types';

let initialized = false;

/**
 * Initialize the app system - fetch initial data and connect SSE
 * Should be called once when the app starts (e.g., in +layout.svelte)
 */
export async function initApps(): Promise<void> {
	if (initialized) return;
	initialized = true;

	// Fetch initial data via GET - reliable and no race conditions
	try {
		const appList = await fetchInstalledApps();
		apps.set(appList);
		loading.set(false);
		error.set(null);
	} catch (err) {
		console.error('Failed to fetch initial apps:', err);
		error.set(err instanceof Error ? err.message : 'Failed to connect to server');
		loading.set(false);
	}

	// Connect SSE for real-time updates
	connectSSE({
		onApps: (appList) => {
			apps.set(appList);
			error.set(null);
			// Refresh layout from backend to pick up any app additions/removals
			layout.refresh();
		},
		onError: () => {
			// SSE handles reconnection internally
		}
	});
}

/**
 * Disconnect from SSE and reset state
 * Call when cleaning up (e.g., in onDestroy)
 */
export function disconnectApps(): void {
	disconnectSSE();
	initialized = false;
}

/**
 * Install an app with optional integration choices
 *
 * Optimistically adds the app to the layout so it appears immediately.
 * The store will be updated via SSE when the backend state changes.
 */
export async function installApp(
	name: string,
	choices: Record<string, string> = {}
): Promise<InstallResult> {
	// Optimistically add to layout so it shows immediately
	layout.addApp(name);
	return apiInstall(name, choices);
}

/**
 * Uninstall an app with optimistic UI update
 *
 * Sets status to 'uninstalling' immediately for responsive UI,
 * reverts on error. Final state comes via SSE.
 */
export async function uninstallApp(name: string): Promise<UninstallResult> {
	// Optimistic update: set status to 'uninstalling' immediately
	apps.update((current) =>
		current.map((app) =>
			app.name === name ? { ...app, status: 'uninstalling' as const } : app
		)
	);

	try {
		return await apiUninstall(name);
	} catch (err) {
		// Revert optimistic update on error
		apps.update((current) =>
			current.map((app) =>
				app.name === name ? { ...app, status: 'running' as const } : app
			)
		);
		throw err;
	}
}
