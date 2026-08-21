// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * App Facade - Unified interface for app operations
 *
 * Drives the app lifecycle via adaptive polling of GET /api/user/home.
 * Positions are server-owned; the frontend does not manage them directly.
 */

import { get } from 'svelte/store';
import { apps, loading, error } from '$lib/stores/apps';
import { gridElements } from '$lib/stores/grid';
import { startPolling, stopPolling } from '$lib/api/poller';
import {
	installApp as apiInstall,
	uninstallApp as apiUninstall,
	renameApp as apiRename,
	type RenameResult,
} from '$lib/clients/appClient';
import { type IntentResponse, AppStatus, type HomeData } from '$lib/types';

export type { RenameResult };

let initialized = false;

function handleHomeData(data: HomeData): void {
	apps.set(data.apps);
	gridElements.setFromHome(data);
	loading.set(false);
	error.set(null);
}

/**
 * Initialize the app system — start polling GET /api/user/home.
 * Called once when the app starts (in +layout.svelte).
 */
export async function initApps(): Promise<void> {
	if (initialized) return;
	initialized = true;

	loading.set(true);
	startPolling(handleHomeData);
}

/**
 * Stop polling and reset state.
 * Called on layout destroy.
 */
export function disconnectApps(): void {
	stopPolling();
	initialized = false;
}

/**
 * Install an app. Returns once the intent is accepted (202);
 * the orchestrator handles the rest. The next poll will pick up
 * the new app in 'installing' status.
 */
export async function installApp(name: string): Promise<IntentResponse> {
	return apiInstall(name);
}

/**
 * Uninstall an app with optimistic UI update.
 * Sets status to 'uninstalling' immediately for responsive UI,
 * reverts on error. The next poll removes the app from the grid.
 */
export async function uninstallApp(name: string): Promise<IntentResponse> {
	const previousStatus = get(apps).find((a) => a.catalog_id === name)?.status;

	apps.update((current) =>
		current.map((app) => (app.catalog_id === name ? { ...app, status: AppStatus.Uninstalling } : app))
	);

	try {
		return await apiUninstall(name);
	} catch (err) {
		apps.update((current) =>
			current.map((app) =>
				app.catalog_id === name ? { ...app, status: previousStatus ?? app.status } : app
			)
		);
		throw err;
	}
}

/**
 * Rename an app's display name.
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
