import { writable, derived, get } from 'svelte/store';
import { browser } from '$app/environment';
import { getWidgetById, isValidWidgetId } from '$lib/widgets/registry';

const STORAGE_KEY = 'bloud-widget-prefs';

/**
 * Widget preferences stored in localStorage
 */
export interface WidgetPrefs {
	/** IDs of enabled widgets in display order */
	enabled: string[];
	/** Per-widget configuration */
	configs: Record<string, Record<string, unknown>>;
}

/**
 * Default preferences - Service Health enabled by default
 */
export const DEFAULT_PREFS: WidgetPrefs = {
	enabled: ['service-health'],
	configs: {},
};

/**
 * Load preferences from localStorage
 */
export function loadPrefs(): WidgetPrefs {
	if (!browser) {
		return DEFAULT_PREFS;
	}

	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		if (!stored) {
			return DEFAULT_PREFS;
		}

		const parsed = JSON.parse(stored) as WidgetPrefs;

		// Validate and filter out invalid widget IDs
		const validEnabled = parsed.enabled?.filter(isValidWidgetId) ?? DEFAULT_PREFS.enabled;

		return {
			enabled: validEnabled,
			configs: parsed.configs ?? {},
		};
	} catch {
		return DEFAULT_PREFS;
	}
}

/**
 * Save preferences to localStorage
 */
export function savePrefs(prefs: WidgetPrefs): void {
	if (!browser) {
		return;
	}

	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs));
	} catch {
		// Silently fail if localStorage is unavailable
	}
}

/**
 * Create the widget preferences store
 */
function createWidgetPrefsStore() {
	const { subscribe, set, update } = writable<WidgetPrefs>(loadPrefs());

	// Auto-save on changes (browser only)
	if (browser) {
		subscribe((prefs) => {
			savePrefs(prefs);
		});
	}

	return {
		subscribe,

		/**
		 * Enable a widget
		 */
		enableWidget(id: string): void {
			if (!isValidWidgetId(id)) return;

			update((prefs) => {
				if (prefs.enabled.includes(id)) return prefs;

				return {
					...prefs,
					enabled: [...prefs.enabled, id],
				};
			});
		},

		/**
		 * Disable a widget
		 */
		disableWidget(id: string): void {
			update((prefs) => ({
				...prefs,
				enabled: prefs.enabled.filter((w) => w !== id),
			}));
		},

		/**
		 * Toggle a widget's enabled state
		 */
		toggleWidget(id: string): void {
			if (!isValidWidgetId(id)) return;

			update((prefs) => {
				if (prefs.enabled.includes(id)) {
					return {
						...prefs,
						enabled: prefs.enabled.filter((w) => w !== id),
					};
				} else {
					return {
						...prefs,
						enabled: [...prefs.enabled, id],
					};
				}
			});
		},

		/**
		 * Update a widget's configuration
		 */
		setWidgetConfig(id: string, config: Record<string, unknown>): void {
			update((prefs) => ({
				...prefs,
				configs: {
					...prefs.configs,
					[id]: config,
				},
			}));
		},

		/**
		 * Reorder widgets
		 */
		reorder(newOrder: string[]): void {
			update((prefs) => ({
				...prefs,
				enabled: newOrder.filter(isValidWidgetId),
			}));
		},

		/**
		 * Reset to defaults
		 */
		reset(): void {
			set(DEFAULT_PREFS);
		},
	};
}

export const widgetPrefs = createWidgetPrefsStore();

/**
 * Derived store: enabled widget definitions in order
 */
export const enabledWidgets = derived(widgetPrefs, ($prefs) =>
	$prefs.enabled.map(getWidgetById).filter((w): w is NonNullable<typeof w> => w !== undefined)
);

/**
 * Derived store: widget config getter
 */
export function getWidgetConfig(id: string): Record<string, unknown> {
	const prefs = get(widgetPrefs);
	const widget = getWidgetById(id);
	return prefs.configs[id] ?? widget?.defaultConfig ?? {};
}
