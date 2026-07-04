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
	renameApp as apiRename,
	type RenameResult,
} from '$lib/clients/appClient';
import { saveLayout } from '$lib/clients/layoutClient';
import { connectSSE, disconnectSSE } from '$lib/api/sse';
import { type IntentResponse, AppStatus } from '$lib/types';

export type { RenameResult };

/**
 * Rename an app's display name.
 * Wraps the intent-based API response into the RenameResult type for UI callers.
 */
export async function renameApp(appName: string, newDisplayName: string): Promise<RenameResult> {
	try {
		await apiRename(appName, newDisplayName);
		return { success: true };
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Rename failed';
		return { success: false, error: message };
	}
}

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

		// Reconcile layout: add any installed non-system apps that are missing
		// from the layout (e.g. after host-agent restart syncs apps from podman
		// without updating the layout, or after CLI-based installs).
		const currentLayout = get(layout);
		const layoutAppIds = new Set(
			currentLayout.filter((el) => el.type === 'app').map((el) => el.id)
		);
		const missingApps = appList.filter((app) => !app.is_system && !layoutAppIds.has(app.catalog_id));
		for (const app of missingApps) {
			layout.addApp(app.catalog_id);
		}
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
				const appNames = new Set(appList.map((a) => a.catalog_id));
				const next = new Set([...pending].filter((n) => !appNames.has(n)));
				return next.size === pending.size ? pending : next;
			});
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
 * Install an app
 *
 * Adds the app to the layout immediately so it appears on the grid.
 * Marks as pending so the UI can show 'installing' before SSE arrives.
 * Returns once the intent is accepted (202); the reconciler handles the rest.
 */
export async function installApp(name: string): Promise<IntentResponse> {
	// Add to layout so it shows on the grid immediately
	layout.addApp(name);
	// Persist immediately — a 500ms debounced save would lose the new app if SSE
	// reconnects (triggered by the nixos-rebuild) within that window and overwrites
	// the in-memory layout with the stale API state.
	saveLayout(get(layout));

	// Mark as pending so the catalog can show 'installing' before SSE arrives
	pendingInstalls.update((s) => new Set(s).add(name));

	return apiInstall(name);
}

/**
 * Uninstall an app with optimistic UI update
 *
 * Sets status to 'uninstalling' immediately for responsive UI,
 * removes from layout on success. Reverts status on error.
 */
export async function uninstallApp(name: string): Promise<IntentResponse> {
	// Optimistic update: set status to 'uninstalling' immediately
	apps.update((current) =>
		current.map((app) => (app.catalog_id === name ? { ...app, status: AppStatus.Uninstalling } : app))
	);

	try {
		const result = await apiUninstall(name);
		// Remove from layout on successful uninstall
		layout.removeApp(name);
		return result;
	} catch (err) {
		// Revert optimistic update on error
		apps.update((current) =>
			current.map((app) => (app.catalog_id === name ? { ...app, status: AppStatus.Running } : app))
		);
		throw err;
	}
}
