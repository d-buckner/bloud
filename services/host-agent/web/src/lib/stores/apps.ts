/**
 * Apps Store - Single source of truth for installed app state
 *
 * This store mirrors the backend's installed apps table. Updates come via:
 * 1. Initial fetch on app load
 * 2. Real-time SSE updates when app state changes
 *
 * Other modules should use the helper functions to look up app data
 * rather than duplicating app objects in their own state.
 */

import { writable, derived, get } from 'svelte/store';
import type { App, AppStatus } from '$lib/types';

// Core store - mirrors the apps table from the backend (includes system apps)
export const apps = writable<App[]>([]);

// Loading state for initial fetch
export const loading = writable(true);

// Error state
export const error = writable<string | null>(null);

// Derived store - only user-facing apps (excludes system apps like postgres, traefik)
export const userApps = derived(apps, ($apps) => $apps.filter((a) => !a.is_system));

// Derived store - apps visible on home screen (excludes system apps and uninstalling)
export const visibleApps = derived(apps, ($apps) =>
	$apps.filter((a) => !a.is_system && a.status !== 'uninstalling')
);

// Derived store for quick lookup of installed app names (user apps only)
export const installedNames = derived(userApps, ($apps) => new Set($apps.map((a) => a.name)));

// --- Helper functions for app lookup ---
// Use these instead of duplicating app data in other stores

/**
 * Get an app by name from the current store state
 */
export function getApp(name: string): App | undefined {
	return get(apps).find((a) => a.name === name);
}

/**
 * Get the status of an app by name
 */
export function getAppStatus(name: string): AppStatus | undefined {
	return getApp(name)?.status;
}

/**
 * Check if an app exists (is installed)
 */
export function appExists(name: string): boolean {
	return get(apps).some((a) => a.name === name);
}

/**
 * Get multiple apps by name, preserving order
 */
export function getApps(names: string[]): App[] {
	const appMap = new Map(get(apps).map((a) => [a.name, a]));
	return names.map((name) => appMap.get(name)).filter((a): a is App => a !== undefined);
}
