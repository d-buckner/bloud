/**
 * Tabs Store - Manages open app tabs state
 *
 * This store only tracks which apps are open and their navigation paths.
 * It does NOT store App objects - those come from the apps store.
 * This prevents data duplication and ensures consistency when apps update via SSE.
 */

import { writable, derived, get } from 'svelte/store';
import { apps } from './apps';
import type { App } from '$lib/types';

interface TabsState {
	/** Names of open apps, in tab order */
	openNames: string[];
	/** Currently active app name */
	activeName: string | null;
	/** Current navigation path within each app */
	paths: Record<string, string>;
}

function createTabsStore() {
	const { subscribe, update, set } = writable<TabsState>({
		openNames: [],
		activeName: null,
		paths: {}
	});

	return {
		subscribe,

		/**
		 * Open an app tab (adds to tabs if not already open, sets as active)
		 */
		open(appName: string, initialPath: string = '') {
			update((state) => {
				const exists = state.openNames.includes(appName);
				if (!exists) {
					return {
						openNames: [...state.openNames, appName],
						activeName: appName,
						paths: { ...state.paths, [appName]: initialPath }
					};
				}
				return {
					...state,
					activeName: appName
				};
			});
		},

		/**
		 * Close an app tab
		 * Returns the name of the next app to activate (or null if no tabs remain)
		 */
		close(appName: string): string | null {
			let nextActive: string | null = null;

			update((state) => {
				const newNames = state.openNames.filter((n) => n !== appName);
				const newPaths = { ...state.paths };
				delete newPaths[appName];

				// If closing the active app, switch to another or null
				if (state.activeName === appName) {
					nextActive = newNames.length > 0 ? newNames[newNames.length - 1] : null;
				} else {
					nextActive = state.activeName;
				}

				return {
					openNames: newNames,
					activeName: nextActive,
					paths: newPaths
				};
			});

			return nextActive;
		},

		/**
		 * Set the active app (when clicking a tab)
		 */
		setActive(appName: string | null) {
			update((state) => ({
				...state,
				activeName: appName
			}));
		},

		/**
		 * Update the current path for an app
		 */
		setPath(appName: string, path: string) {
			update((state) => ({
				...state,
				paths: { ...state.paths, [appName]: path }
			}));
		},

		/**
		 * Get the current path for an app
		 */
		getPath(appName: string): string {
			const state = get({ subscribe });
			return state.paths[appName] ?? '';
		},

		/**
		 * Check if an app is currently open
		 */
		isOpen(appName: string): boolean {
			return get({ subscribe }).openNames.includes(appName);
		},

		/**
		 * Clear all open tabs
		 */
		clear() {
			set({ openNames: [], activeName: null, paths: {} });
		}
	};
}

export const tabs = createTabsStore();

// --- Derived stores ---

/** List of open app names */
export const openAppNames = derived(tabs, ($state) => $state.openNames);

/** Currently active app name */
export const activeAppName = derived(tabs, ($state) => $state.activeName);

/** Map of app name to current path */
export const appPaths = derived(tabs, ($state) => $state.paths);

/**
 * List of open App objects (derived from apps store)
 * This is the key difference from the old openApps store - we don't store
 * App objects, we derive them. This ensures they're always in sync with SSE updates.
 */
export const openAppsList = derived([tabs, apps], ([$tabs, $apps]) => {
	const appMap = new Map($apps.map((a) => [a.name, a]));
	return $tabs.openNames
		.map((name) => appMap.get(name))
		.filter((a): a is App => a !== undefined);
});

/** The currently active App object */
export const activeApp = derived([tabs, apps], ([$tabs, $apps]) => {
	if (!$tabs.activeName) return null;
	return $apps.find((a) => a.name === $tabs.activeName) ?? null;
});
