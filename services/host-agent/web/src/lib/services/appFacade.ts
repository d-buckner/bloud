// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * App Facade - Unified interface for app operations
 *
 * Drives the app lifecycle via adaptive polling of GET /api/user/home.
 * Positions are server-owned; the frontend does not manage them directly.
 */

import { get } from 'svelte/store';
import { apps, loading } from '$lib/stores/apps';
import { gridElements } from '$lib/stores/grid';
import { startAppEvents, stopAppEvents } from '$lib/api/appEvents';
import {
	installApp as apiInstall,
	uninstallApp as apiUninstall,
	renameApp as apiRename,
	type RenameResult,
} from '$lib/clients/appClient';
import { type App, type IntentResponse, AppStatus } from '$lib/types';

export type { RenameResult };

let initialized = false;

/**
 * Initialize the app system — open the SSE event stream (primary source)
 * with the adaptive poller kept as a watchdog-armed safety net.
 * Called once when the app starts (in +layout.svelte).
 */
export async function initApps(): Promise<void> {
	if (initialized) return;
	initialized = true;

	loading.set(true);
	startAppEvents();
}

/**
 * Stop the event stream (and any fallback polling) and reset state.
 * Called on layout destroy.
 */
export function disconnectApps(): void {
	stopAppEvents();
	initialized = false;
}

/**
 * Install an app. Returns once the intent is accepted (202);
 * the orchestrator handles the rest.
 *
 * The 202 response carries the installing app record (the orchestrator
 * records it at submit time), which is applied to the stores immediately —
 * the tile appears without waiting for the next snapshot. If the record is
 * missing (store write failed server-side), a synthetic installing entry is
 * inserted as an optimistic fallback.
 */
export async function installApp(name: string): Promise<IntentResponse> {
	const res = await apiInstall(name);
	applyInstalledApp(res.app ?? null, name);
	return res;
}

function applyInstalledApp(app: App | null, name: string): void {
	const now = new Date().toISOString();
	const entry: App = app ?? {
		id: 0,
		catalog_id: name,
		display_name: name,
		version: '',
		status: AppStatus.Installing,
		is_system: false,
		installed_at: now,
		updated_at: now
	};
	apps.update((current) => upsertApp(current, entry));
	gridElements.addApp(entry.catalog_id);
}

function upsertApp(current: App[], entry: App): App[] {
	const idx = current.findIndex((a) => a.catalog_id === entry.catalog_id);
	if (idx === -1) return [...current, entry];
	const next = [...current];
	next[idx] = entry;
	return next;
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
