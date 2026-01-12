import { writable, derived, get } from 'svelte/store';
import { goto } from '$app/navigation';
import type { App } from '$lib/types';

interface OpenAppsState {
	apps: App[];
	activeAppName: string | null;
	paths: Record<string, string>; // appName -> current path within the app
}

function createOpenAppsStore() {
	const { subscribe, update, set } = writable<OpenAppsState>({
		apps: [],
		activeAppName: null,
		paths: {}
	});

	return {
		subscribe,

		// Open an app (adds to tabs if not already open, sets as active)
		open(app: App, initialPath: string = '') {
			update((state) => {
				const exists = state.apps.some((a) => a.name === app.name);
				if (!exists) {
					return {
						apps: [...state.apps, app],
						activeAppName: app.name,
						paths: { ...state.paths, [app.name]: initialPath }
					};
				}
				return {
					...state,
					activeAppName: app.name
				};
			});
		},

		// Close an app tab
		close(appName: string) {
			update((state) => {
				const newApps = state.apps.filter((a) => a.name !== appName);
				const newPaths = { ...state.paths };
				delete newPaths[appName];
				let newActive = state.activeAppName;

				// If closing the active app, switch to another or null
				if (state.activeAppName === appName) {
					newActive = newApps.length > 0 ? newApps[newApps.length - 1].name : null;
				}

				return {
					apps: newApps,
					activeAppName: newActive,
					paths: newPaths
				};
			});
		},

		// Set active app (when clicking a tab)
		setActive(appName: string | null) {
			update((state) => ({
				...state,
				activeAppName: appName
			}));
		},

		// Update the current path for an app
		setPath(appName: string, path: string) {
			update((state) => ({
				...state,
				paths: { ...state.paths, [appName]: path }
			}));
		},

		// Get the current path for an app
		getPath(appName: string): string {
			const state = get({ subscribe });
			return state.paths[appName] ?? '';
		},

		// Update an app's data (when status changes via SSE)
		updateApp(updatedApp: App) {
			update((state) => ({
				...state,
				apps: state.apps.map((a) => (a.name === updatedApp.name ? updatedApp : a))
			}));
		},

		// Clear all open apps
		clear() {
			set({ apps: [], activeAppName: null, paths: {} });
		}
	};
}

export const openApps = createOpenAppsStore();

// Action to open an app - updates state and navigates
export function openApp(app: App | string) {
	const appName = typeof app === 'string' ? app : app.name;

	// If passed an App object, add to open apps list
	if (typeof app !== 'string') {
		openApps.open(app);
	} else {
		// Just set as active (assumes already in list)
		openApps.setActive(appName);
	}

	goto(`/apps/${appName}`);
}

// Action to close an app tab and navigate if needed
export function closeApp(appName: string, currentPath: string) {
	const state = get(openApps);
	const remaining = state.apps.filter(a => a.name !== appName);

	openApps.close(appName);

	// If we're on this app's page, navigate away
	if (currentPath.startsWith(`/apps/${appName}`)) {
		if (remaining.length > 0) {
			const nextApp = remaining[remaining.length - 1].name;
			goto(`/apps/${nextApp}`);
		} else {
			goto('/');
		}
	}
}

// Derived store for just the list of open apps
export const openAppsList = derived(openApps, ($state) => $state.apps);

// Derived store for active app name
export const activeAppName = derived(openApps, ($state) => $state.activeAppName);

// Derived store for the active app object
export const activeApp = derived(openApps, ($state) =>
	$state.apps.find((a) => a.name === $state.activeAppName) ?? null
);

// Derived store for app paths
export const appPaths = derived(openApps, ($state) => $state.paths);
