/**
 * App Facade - Unified interface for app operations
 *
 * This facade provides a single import point for all app operations.
 * Internal implementation details (SSE, lifecycle, API calls) are hidden.
 *
 * Usage:
 *   import { initApps, installApp, uninstallApp, renameApp } from '$lib/services/appFacade';
 *
 * Layout ownership: The frontend fully owns layout computation. When an app is
 * installed, we add it to the layout. When uninstalled, we remove it. The backend
 * just stores the layout as opaque JSON.
 */

import { apps, loading, error } from '$lib/stores/apps';
import { layout } from '$lib/stores/layout';
import {
	fetchInstalledApps,
	installApp as apiInstall,
	uninstallApp as apiUninstall,
	renameApp,
	type RenameResult,
} from '$lib/clients/appClient';
import { connectSSE, disconnectSSE } from '$lib/api/sse';
import type { InstallResult, UninstallResult } from '$lib/types';

export { renameApp, type RenameResult };

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
			// Refresh layout for cross-device sync
			layout.refresh();
		},
		onError: () => {
			// SSE handles reconnection internally
		},
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
 * Adds the app to the layout immediately so it appears on the grid.
 * App status (installing, running, etc.) is tracked in the apps store.
 */
export async function installApp(
	name: string,
	choices: Record<string, string> = {}
): Promise<InstallResult> {
	// Add to layout so it shows on the grid immediately
	layout.addApp(name);
	return apiInstall(name, choices);
}

/**
 * Uninstall an app with optimistic UI update
 *
 * Sets status to 'uninstalling' immediately for responsive UI,
 * removes from layout on success. Reverts status on error.
 */
export async function uninstallApp(name: string): Promise<UninstallResult> {
	// Optimistic update: set status to 'uninstalling' immediately
	apps.update((current) =>
		current.map((app) => (app.name === name ? { ...app, status: 'uninstalling' as const } : app))
	);

	try {
		const result = await apiUninstall(name);
		// Remove from layout on successful uninstall
		layout.removeApp(name);
		return result;
	} catch (err) {
		// Revert optimistic update on error
		apps.update((current) =>
			current.map((app) => (app.name === name ? { ...app, status: 'running' as const } : app))
		);
		throw err;
	}
}
