import { writable, derived, get } from 'svelte/store';
import { browser } from '$app/environment';
import { getWidgetById, isValidWidgetId } from '$lib/widgets/registry';
import { fetchLayout, saveLayout, type Layout } from '$lib/clients/layoutClient';

const STORAGE_KEY = 'bloud-layout';
const GRID_COLS = 6;

/**
 * A grid element with explicit positioning (0-based, matches GridStack natively)
 */
export interface GridElement {
	type: 'app' | 'widget';
	id: string; // app name or widget id
	x: number; // 0-based column position
	y: number; // 0-based row position
	w: number; // width in columns
	h: number; // height in rows
}

/**
 * Default layout - System stats widget enabled
 */
export const DEFAULT_LAYOUT: GridElement[] = [
	{ type: 'widget', id: 'system-stats', x: 0, y: 0, w: 2, h: 3 },
];

/**
 * Check if a cell is occupied by any element
 */
function isCellOccupied(
	elements: GridElement[],
	x: number,
	y: number,
	excludeId?: string
): boolean {
	return elements.some((el) => {
		if (excludeId && el.id === excludeId) return false;
		const endX = el.x + el.w - 1;
		const endY = el.y + el.h - 1;
		return x >= el.x && x <= endX && y >= el.y && y <= endY;
	});
}

/**
 * Find the next available position for an element of given size
 */
function findNextAvailablePosition(
	elements: GridElement[],
	w: number,
	h: number
): { x: number; y: number } {
	const maxY = elements.reduce((max, el) => Math.max(max, el.y + el.h - 1), -1);

	for (let y = 0; y <= maxY + 10; y++) {
		for (let x = 0; x <= GRID_COLS - w; x++) {
			let canPlace = true;
			for (let cx = x; cx < x + w && canPlace; cx++) {
				for (let cy = y; cy < y + h && canPlace; cy++) {
					if (isCellOccupied(elements, cx, cy)) {
						canPlace = false;
					}
				}
			}
			if (canPlace) {
				return { x, y };
			}
		}
	}

	return { x: 0, y: maxY + 1 };
}

/**
 * Check if data is a valid layout response shape
 */
function isLayoutData(data: unknown): data is Layout {
	if (Array.isArray(data)) return true;
	if (data && typeof data === 'object' && 'elements' in data) {
		return Array.isArray((data as { elements: unknown }).elements);
	}
	return false;
}

/**
 * Detect and migrate old 1-based col/row/colspan/rowspan format to 0-based x/y/w/h
 */
function migrateElement(el: Record<string, unknown>): GridElement {
	// Old format has col/row/colspan/rowspan (1-based)
	if ('col' in el || 'row' in el || 'colspan' in el || 'rowspan' in el) {
		return {
			type: el.type as 'app' | 'widget',
			id: el.id as string,
			x: ((el.col as number) ?? 1) - 1,
			y: ((el.row as number) ?? 1) - 1,
			w: (el.colspan as number) ?? 1,
			h: (el.rowspan as number) ?? 1,
		};
	}
	// New format already has x/y/w/h
	return {
		type: el.type as 'app' | 'widget',
		id: el.id as string,
		x: (el.x as number) ?? 0,
		y: (el.y as number) ?? 0,
		w: (el.w as number) ?? 1,
		h: (el.h as number) ?? 1,
	};
}

/**
 * Normalize layout loaded from any source (localStorage or API)
 */
function normalizeLayout(data: Layout): GridElement[] {
	// Handle both old format {elements: [...]} and new format [...]
	const elements = Array.isArray(data) ? data : data.elements;

	return elements
		.filter((el): el is Record<string, unknown> => {
			if (!el || typeof el !== 'object') return false;
			const item = el as Record<string, unknown>;
			if (item.type === 'widget') {
				return isValidWidgetId(item.id as string);
			}
			return item.type === 'app' && typeof item.id === 'string';
		})
		.map((el) => {
			const migrated = migrateElement(el as Record<string, unknown>);
			// For widgets, lock w/h to the registered widget size
			if (migrated.type === 'widget') {
				const widget = getWidgetById(migrated.id);
				if (widget) {
					return {
						...migrated,
						w: widget.size.cols,
						h: widget.size.rows,
					};
				}
			}
			return migrated;
		});
}

/**
 * Load layout from localStorage
 */
function loadLayoutFromLocalStorage(): GridElement[] {
	if (!browser) return DEFAULT_LAYOUT;

	try {
		const stored = localStorage.getItem(STORAGE_KEY);
		if (stored) {
			const parsed: unknown = JSON.parse(stored);
			if (isLayoutData(parsed)) {
				return normalizeLayout(parsed);
			}
		}
		return DEFAULT_LAYOUT;
	} catch {
		return DEFAULT_LAYOUT;
	}
}

/**
 * Save layout to localStorage
 */
function saveLayoutToLocalStorage(elements: GridElement[]): void {
	if (!browser) return;
	try {
		localStorage.setItem(STORAGE_KEY, JSON.stringify(elements));
	} catch {
		// Silently fail if localStorage is unavailable
	}
}

let saveTimeout: ReturnType<typeof setTimeout> | null = null;

/**
 * Create the layout store
 */
function createLayoutStore() {
	const { subscribe, set, update } = writable<GridElement[]>(loadLayoutFromLocalStorage());

	let initialized = false;

	if (browser) {
		fetchLayout().then((data) => {
			const apiLayout = data ? normalizeLayout(data) : null;
			if (apiLayout && apiLayout.length > 0) {
				set(apiLayout);
				saveLayoutToLocalStorage(apiLayout);
			}
			initialized = true;
		});

		subscribe((elements) => {
			saveLayoutToLocalStorage(elements);

			if (initialized) {
				if (saveTimeout) clearTimeout(saveTimeout);
				saveTimeout = setTimeout(() => {
					saveLayout(elements);
				}, 500);
			}
		});
	}

	return {
		subscribe,

		async refresh(): Promise<void> {
			const data = await fetchLayout();
			if (data) {
				set(normalizeLayout(data));
			}
		},

		setElements(newElements: GridElement[]): void {
			set(newElements);
		},

		moveElement(elementId: string, x: number, y: number): void {
			update((elements) =>
				elements.map((el) => (el.id === elementId ? { ...el, x, y } : el))
			);
		},

		resizeElement(elementId: string, w: number, h: number): void {
			update((elements) =>
				elements.map((el) => (el.id === elementId ? { ...el, w, h } : el))
			);
		},

		addWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;

			update((elements) => {
				if (elements.some((el) => el.type === 'widget' && el.id === widgetId)) {
					return elements;
				}

				const widget = getWidgetById(widgetId);
				const w = widget?.size.cols ?? 2;
				const h = widget?.size.rows ?? 2;
				const { x, y } = findNextAvailablePosition(elements, w, h);

				return [...elements, { type: 'widget', id: widgetId, x, y, w, h }];
			});
		},

		addApp(appName: string): void {
			update((elements) => {
				if (elements.some((el) => el.id === appName)) {
					return elements;
				}

				const { x, y } = findNextAvailablePosition(elements, 1, 1);
				return [...elements, { type: 'app', id: appName, x, y, w: 1, h: 1 }];
			});
		},

		removeWidget(widgetId: string): void {
			update((elements) => elements.filter((el) => !(el.type === 'widget' && el.id === widgetId)));
		},

		removeApp(appName: string): void {
			update((elements) => elements.filter((el) => !(el.type === 'app' && el.id === appName)));
		},

		toggleWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;

			update((elements) => {
				const exists = elements.some((el) => el.type === 'widget' && el.id === widgetId);
				if (exists) {
					return elements.filter((el) => !(el.type === 'widget' && el.id === widgetId));
				}

				const widget = getWidgetById(widgetId);
				const w = widget?.size.cols ?? 2;
				const h = widget?.size.rows ?? 2;
				const { x, y } = findNextAvailablePosition(elements, w, h);

				return [...elements, { type: 'widget', id: widgetId, x, y, w, h }];
			});
		},

		reset(): void {
			set(DEFAULT_LAYOUT);
		},
	};
}

export const layout = createLayoutStore();

/**
 * Check if a widget is enabled
 */
export function isWidgetEnabled(widgetId: string): boolean {
	const elements = get(layout);
	return elements.some((el) => el.type === 'widget' && el.id === widgetId);
}

/**
 * Reactive derived store for enabled widgets (for widget picker)
 */
export const enabledWidgetIds = derived(layout, ($layout) =>
	$layout.filter((el) => el.type === 'widget').map((el) => el.id)
);

// Layout is fully owned by the frontend:
// - addApp() is called when user clicks "Get" to install an app
// - removeApp() is called when user uninstalls an app
// - Backend just stores/retrieves layout as opaque JSON
