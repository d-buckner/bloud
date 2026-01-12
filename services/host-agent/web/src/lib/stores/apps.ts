import { writable, derived } from 'svelte/store';
import type { App } from '$lib/types';

// Core store - mirrors the apps table from the backend (includes system apps)
export const apps = writable<App[]>([]);

// Derived store - only user-facing apps (excludes system apps like postgres, traefik)
export const userApps = derived(apps, ($apps) => $apps.filter((a) => !a.is_system));

// Derived store - apps visible on home screen (excludes system apps and uninstalling)
export const visibleApps = derived(apps, ($apps) =>
	$apps.filter((a) => !a.is_system && a.status !== 'uninstalling')
);

// Loading state
export const loading = writable(true);

// Error state
export const error = writable<string | null>(null);

// Derived store for quick lookup of installed app names (user apps only)
export const installedNames = derived(userApps, ($apps) => new Set($apps.map((a) => a.name)));
