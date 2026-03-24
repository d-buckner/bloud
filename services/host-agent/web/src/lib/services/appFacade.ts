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
import { get, writable } from 'svelte/store';

// Names of apps whose install has been triggered but not yet reflected in the apps store via SSE
export const pendingInstalls = writable<Set<string>>(new Set());
import { layout } from '$lib/stores/layout';
import {
	fetchInstalledApps,
	installApp as apiInstall,
	uninstallApp as apiUninstall,
	renameApp,
	type RenameResult,
} from '$lib/clients/appClient';
import { saveLayout } from '$lib/clients/layoutClient';
import { connectSSE, disconnectSSE } from '$lib/api/sse';
import { type InstallResult, type UninstallResult, AppStatus } from '$lib/types';

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
			// Clear pending installs that now have real status from the backend
			pendingInstalls.update((pending) => {
				if (pending.size === 0) return pending;
				const appNames = new Set(appList.map((a) => a.name));
				const next = new Set([...pending].filter((n) => !appNames.has(n)));
				return next.size === pending.size ? pending : next;
			});
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
 * Marks as pending so the UI can show 'installing' before SSE arrives.
 */
export async function installApp(
	name: string,
	choices: Record<string, string> = {}
): Promise<InstallResult> {
	// Add to layout so it shows on the grid immediately
	layout.addApp(name);
	// Persist immediately — a 500ms debounced save would lose the new app if SSE
	// reconnects (triggered by the nixos-rebuild) within that window and overwrites
	// the in-memory layout with the stale API state.
	saveLayout(get(layout));

	// Mark as pending so the catalog can show 'installing' before SSE arrives
	pendingInstalls.update((s) => new Set(s).add(name));

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
		current.map((app) => (app.name === name ? { ...app, status: AppStatus.Uninstalling } : app))
	);

	try {
		const result = await apiUninstall(name);
		// Remove from layout on successful uninstall
		layout.removeApp(name);
		return result;
	} catch (err) {
		// Revert optimistic update on error
		apps.update((current) =>
			current.map((app) => (app.name === name ? { ...app, status: AppStatus.Running } : app))
		);
		throw err;
	}
}
