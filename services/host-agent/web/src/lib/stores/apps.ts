// SPDX-License-Identifier: AGPL-3.0-only
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

import { writable, derived } from 'svelte/store';
import type { App } from '$lib/types';

// Core store - mirrors the apps table from the backend (includes system apps)
export const apps = writable<App[]>([]);

// Loading state for initial fetch
export const loading = writable(true);

// Error state
export const error = writable<string | null>(null);

// Derived store - apps visible on home screen.
//
// Three exclusions: system apps, apps mid-uninstall, and headless apps. The
// headless case is the app that has nothing to open (affine-mcp serves an MCP
// endpoint and no page), so a tile would only be a broken link. It stays in the
// `apps` store above, where its status still drives the toasts and the install
// progress view.
export const visibleApps = derived(apps, ($apps) => $apps.filter(isGridApp));

// isGridApp is the single answer to "does this app get a tile?". Exported pure
// so the rule is testable without a store round trip.
export function isGridApp(app: App): boolean {
	return !app.is_system && !app.headless && app.status !== 'uninstalling';
}
