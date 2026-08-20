// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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

// Derived store - apps visible on home screen (excludes system apps and uninstalling)
export const visibleApps = derived(apps, ($apps) =>
	$apps.filter((a) => !a.is_system && a.status !== 'uninstalling')
);
