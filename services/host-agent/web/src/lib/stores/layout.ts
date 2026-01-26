import { writable, derived, get } from 'svelte/store';
import { browser } from '$app/environment';
import { getWidgetById, isValidWidgetId } from '$lib/widgets/registry';

const STORAGE_KEY = 'bloud-layout';
const GRID_COLS = 6;

/**
 * A grid item with explicit positioning
 */
export interface GridItem {
	type: 'app' | 'widget';
	id: string; // app name or widget id
	col: number; // 1-based column position
	row: number; // 1-based row position
	colspan: number; // number of columns to span
	rowspan: number; // number of rows to span
}

/**
 * Layout stored in localStorage and backend
 */
export interface Layout {
	items: GridItem[];
	widgetConfigs: Record<string, Record<string, unknown>>;
}

/**
 * Default layout - System stats widget enabled
 */
export const DEFAULT_LAYOUT: Layout = {
	items: [{ type: 'widget', id: 'system-stats', col: 1, row: 1, colspan: 2, rowspan: 3 }],
	widgetConfigs: {},
};

/**
 * Check if a cell is occupied by any item
 */
function isCellOccupied(
	items: GridItem[],
	col: number,
	row: number,
	excludeId?: string
): boolean {
	return items.some((item) => {
		if (excludeId && item.id === excludeId) return false;
		const itemEndCol = item.col + item.colspan - 1;
		const itemEndRow = item.row + item.rowspan - 1;
		return col >= item.col && col <= itemEndCol && row >= item.row && row <= itemEndRow;
	});
}

/**
 * Find the next available position for an item of given size
 */
function findNextAvailablePosition(
	items: GridItem[],
	colspan: number,
	rowspan: number
): { col: number; row: number } {
	// Find the maximum row currently in use
	const maxRow = items.reduce((max, item) => Math.max(max, item.row + item.rowspan - 1), 0);

	// Search for available space row by row
	for (let row = 1; row <= maxRow + 10; row++) {
		for (let col = 1; col <= GRID_COLS - colspan + 1; col++) {
			// Check if all cells needed for this item are free
			let canPlace = true;
			for (let c = col; c < col + colspan && canPlace; c++) {
				for (let r = row; r < row + rowspan && canPlace; r++) {
					if (isCellOccupied(items, c, r)) {
						canPlace = false;
					}
				}
			}
			if (canPlace) {
				return { col, row };
			}
		}
	}

	// Fallback: place at the end
	return { col: 1, row: maxRow + 1 };
}

/**
 * Normalize layout loaded from any source (localStorage or API)
 */
function normalizeLayout(layout: Layout): Layout {
	const validItems = layout.items
		.filter((item) => {
			if (item.type === 'widget') {
				return isValidWidgetId(item.id);
			}
			return true; // Apps are validated against live data
		})
		.map((item) => {
			// For widgets, always use registry size (migration for old data)
			if (item.type === 'widget') {
				const widget = getWidgetById(item.id);
				if (widget) {
					return {
						...item,
						col: item.col ?? 1,
						row: item.row ?? 1,
						colspan: widget.size.cols,
						rowspan: widget.size.rows,
					};
				}
			}
			// For apps, use 1x1
			return {
				...item,
				col: item.col ?? 1,
				row: item.row ?? 1,
				colspan: 1,
				rowspan: 1,
			};
		});
	return {
		items: validItems,
		widgetConfigs: layout.widgetConfigs ?? {},
	};
}

/**
 * Load layout from localStorage (used as cache/fallback)
 */
function loadLayoutFromLocalStorage(): Layout {
	if (!browser) {
		return DEFAULT_LAYOUT;
	}

	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		if (stored) {
			const parsed = JSON.parse(stored) as Layout;
			return normalizeLayout(parsed);
		}
		return DEFAULT_LAYOUT;
	} catch {
		return DEFAULT_LAYOUT;
	}
}

/**
 * Save layout to localStorage
 */
function saveLayoutToLocalStorage(layout: Layout): void {
	if (!browser) return;

	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(layout));
	} catch {
		// Silently fail if localStorage is unavailable
	}
}

/**
 * Fetch layout from backend API
 */
async function fetchLayoutFromAPI(): Promise<Layout | null> {
	try {
		const res = await fetch('/api/user/layout');
		if (!res.ok) {
			if (res.status === 401) {
				// Not authenticated - use localStorage
				return null;
			}
			throw new Error(`Failed to fetch layout: ${res.status}`);
		}
		const data = await res.json();
		return normalizeLayout(data);
	} catch (err) {
		console.error('Failed to fetch layout from API:', err);
		return null;
	}
}

/**
 * Save layout to backend API
 */
async function saveLayoutToAPI(layout: Layout): Promise<void> {
	try {
		const res = await fetch('/api/user/layout', {
			method: 'PUT',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify(layout),
		});
		if (!res.ok && res.status !== 401) {
			console.error('Failed to save layout to API:', res.status);
		}
	} catch (err) {
		console.error('Failed to save layout to API:', err);
	}
}

// Debounce timer for API saves
let saveTimeout: ReturnType<typeof setTimeout> | null = null;

/**
 * Create the layout store
 */
function createLayoutStore() {
	// Start with localStorage data (fast), then fetch from API
	const { subscribe, set, update } = writable<Layout>(loadLayoutFromLocalStorage());

	// Track if we've initialized from API
	let initialized = false;

	// Initialize from API when in browser
	if (browser) {
		fetchLayoutFromAPI().then((apiLayout) => {
			if (apiLayout && apiLayout.items.length > 0) {
				set(apiLayout);
				saveLayoutToLocalStorage(apiLayout); // Update cache
			}
			initialized = true;
		});

		// Auto-save on changes
		subscribe((layout) => {
			// Always save to localStorage immediately
			saveLayoutToLocalStorage(layout);

			// Debounce API saves (500ms)
			if (initialized) {
				if (saveTimeout) clearTimeout(saveTimeout);
				saveTimeout = setTimeout(() => {
					saveLayoutToAPI(layout);
				}, 500);
			}
		});
	}

	return {
		subscribe,

		/**
		 * Force refresh from API
		 */
		async refresh(): Promise<void> {
			const apiLayout = await fetchLayoutFromAPI();
			if (apiLayout) {
				set(apiLayout);
			}
		},

		/**
		 * Update all grid items
		 */
		setItems(newItems: GridItem[]): void {
			update((layout) => ({
				...layout,
				items: newItems,
			}));
		},

		/**
		 * Move an item to a new position
		 */
		moveItem(itemId: string, col: number, row: number): void {
			update((layout) => {
				const items = layout.items.map((item) => {
					if (item.id === itemId) {
						return { ...item, col, row };
					}
					return item;
				});
				return { ...layout, items };
			});
		},

		/**
		 * Resize an item
		 */
		resizeItem(itemId: string, colspan: number, rowspan: number): void {
			update((layout) => {
				const items = layout.items.map((item) => {
					if (item.id === itemId) {
						return { ...item, colspan, rowspan };
					}
					return item;
				});
				return { ...layout, items };
			});
		},

		/**
		 * Add a widget to the grid at the next available position
		 */
		addWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;

			update((layout) => {
				// Don't add duplicates
				const exists = layout.items.some((item) => item.type === 'widget' && item.id === widgetId);
				if (exists) return layout;

				// Get widget size from registry
				const widget = getWidgetById(widgetId);
				const colspan = widget?.size.cols ?? 2;
				const rowspan = widget?.size.rows ?? 2;

				// Find next available position
				const { col, row } = findNextAvailablePosition(layout.items, colspan, rowspan);

				return {
					...layout,
					items: [...layout.items, { type: 'widget', id: widgetId, col, row, colspan, rowspan }],
				};
			});
		},

		/**
		 * Add an app to the grid at the next available position (optimistic)
		 * Used when install starts - backend will also add it, but this shows it immediately
		 */
		addApp(appName: string): void {
			update((layout) => {
				// Don't add duplicates
				const exists = layout.items.some((item) => item.type === 'app' && item.id === appName);
				if (exists) return layout;

				// Apps are always 1x1
				const { col, row } = findNextAvailablePosition(layout.items, 1, 1);

				return {
					...layout,
					items: [...layout.items, { type: 'app', id: appName, col, row, colspan: 1, rowspan: 1 }],
				};
			});
		},

		/**
		 * Remove a widget from the grid
		 */
		removeWidget(widgetId: string): void {
			update((layout) => ({
				...layout,
				items: layout.items.filter((item) => !(item.type === 'widget' && item.id === widgetId)),
			}));
		},

		/**
		 * Toggle a widget's presence in the grid
		 */
		toggleWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;

			update((layout) => {
				const exists = layout.items.some((item) => item.type === 'widget' && item.id === widgetId);
				if (exists) {
					return {
						...layout,
						items: layout.items.filter((item) => !(item.type === 'widget' && item.id === widgetId)),
					};
				}

				// Get widget size from registry
				const widget = getWidgetById(widgetId);
				const colspan = widget?.size.cols ?? 2;
				const rowspan = widget?.size.rows ?? 2;

				// Find next available position
				const { col, row } = findNextAvailablePosition(layout.items, colspan, rowspan);

				return {
					...layout,
					items: [...layout.items, { type: 'widget', id: widgetId, col, row, colspan, rowspan }],
				};
			});
		},

		/**
		 * Update a widget's configuration
		 */
		setWidgetConfig(widgetId: string, config: Record<string, unknown>): void {
			update((layout) => ({
				...layout,
				widgetConfigs: {
					...layout.widgetConfigs,
					[widgetId]: config,
				},
			}));
		},

		/**
		 * Reset to defaults
		 */
		reset(): void {
			set(DEFAULT_LAYOUT);
		},
	};
}

export const layout = createLayoutStore();

/**
 * Get widget config with fallback to defaults
 */
export function getWidgetConfig(widgetId: string): Record<string, unknown> {
	const currentLayout = get(layout);
	const widget = getWidgetById(widgetId);
	return currentLayout.widgetConfigs[widgetId] ?? widget?.defaultConfig ?? {};
}

/**
 * Check if a widget is enabled
 */
export function isWidgetEnabled(widgetId: string): boolean {
	const currentLayout = get(layout);
	return currentLayout.items.some((item) => item.type === 'widget' && item.id === widgetId);
}

/**
 * Reactive derived store for enabled widgets (for widget picker)
 */
export const enabledWidgetIds = derived(layout, ($layout) =>
	$layout.items.filter((item) => item.type === 'widget').map((item) => item.id)
);

// Note: App add/remove is now handled by the backend on install/uninstall.
// The frontend just needs to refresh from SSE or refetch when apps change.
