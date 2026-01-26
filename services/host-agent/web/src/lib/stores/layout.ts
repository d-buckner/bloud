import { writable, derived, get } from 'svelte/store';
import { browser } from '$app/environment';
import { getWidgetById, isValidWidgetId } from '$lib/widgets/registry';
import { fetchLayout, saveLayout, type Layout } from '$lib/clients/layoutClient';

const STORAGE_KEY = 'bloud-layout';
const GRID_COLS = 6;

/**
 * A grid element with explicit positioning
 */
export interface GridElement {
	type: 'app' | 'widget';
	id: string; // app name or widget id
	col: number; // 1-based column position
	row: number; // 1-based row position
	colspan: number; // number of columns to span
	rowspan: number; // number of rows to span
}

/**
 * Default layout - System stats widget enabled
 */
export const DEFAULT_LAYOUT: GridElement[] = [
	{ type: 'widget', id: 'system-stats', col: 1, row: 1, colspan: 2, rowspan: 3 },
];

/**
 * Check if a cell is occupied by any element
 */
function isCellOccupied(
	elements: GridElement[],
	col: number,
	row: number,
	excludeId?: string
): boolean {
	return elements.some((el) => {
		if (excludeId && el.id === excludeId) return false;
		const endCol = el.col + el.colspan - 1;
		const endRow = el.row + el.rowspan - 1;
		return col >= el.col && col <= endCol && row >= el.row && row <= endRow;
	});
}

/**
 * Find the next available position for an element of given size
 */
function findNextAvailablePosition(
	elements: GridElement[],
	colspan: number,
	rowspan: number
): { col: number; row: number } {
	const maxRow = elements.reduce((max, el) => Math.max(max, el.row + el.rowspan - 1), 0);

	for (let row = 1; row <= maxRow + 10; row++) {
		for (let col = 1; col <= GRID_COLS - colspan + 1; col++) {
			let canPlace = true;
			for (let c = col; c < col + colspan && canPlace; c++) {
				for (let r = row; r < row + rowspan && canPlace; r++) {
					if (isCellOccupied(elements, c, r)) {
						canPlace = false;
					}
				}
			}
			if (canPlace) {
				return { col, row };
			}
		}
	}

	return { col: 1, row: maxRow + 1 };
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
 * Normalize layout loaded from any source (localStorage or API)
 */
function normalizeLayout(data: Layout): GridElement[] {
	// Handle both old format {elements: [...]} and new format [...]
	const elements = Array.isArray(data) ? data : data.elements;

	return elements
		.filter((el): el is GridElement => {
			if (!el || typeof el !== 'object') return false;
			const item = el as GridElement;
			if (item.type === 'widget') {
				return isValidWidgetId(item.id);
			}
			return item.type === 'app' && typeof item.id === 'string';
		})
		.map((el) => {
			if (el.type === 'widget') {
				const widget = getWidgetById(el.id);
				if (widget) {
					return {
						...el,
						col: el.col ?? 1,
						row: el.row ?? 1,
						colspan: widget.size.cols,
						rowspan: widget.size.rows,
					};
				}
			}
			return {
				...el,
				col: el.col ?? 1,
				row: el.row ?? 1,
				colspan: 1,
				rowspan: 1,
			};
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

		moveElement(elementId: string, col: number, row: number): void {
			update((elements) =>
				elements.map((el) => (el.id === elementId ? { ...el, col, row } : el))
			);
		},

		resizeElement(elementId: string, colspan: number, rowspan: number): void {
			update((elements) =>
				elements.map((el) => (el.id === elementId ? { ...el, colspan, rowspan } : el))
			);
		},

		addWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;

			update((elements) => {
				if (elements.some((el) => el.type === 'widget' && el.id === widgetId)) {
					return elements;
				}

				const widget = getWidgetById(widgetId);
				const colspan = widget?.size.cols ?? 2;
				const rowspan = widget?.size.rows ?? 2;
				const { col, row } = findNextAvailablePosition(elements, colspan, rowspan);

				return [...elements, { type: 'widget', id: widgetId, col, row, colspan, rowspan }];
			});
		},

		addApp(appName: string): void {
			update((elements) => {
				if (elements.some((el) => el.id === appName)) {
					return elements;
				}

				const { col, row } = findNextAvailablePosition(elements, 1, 1);
				return [...elements, { type: 'app', id: appName, col, row, colspan: 1, rowspan: 1 }];
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
				const colspan = widget?.size.cols ?? 2;
				const rowspan = widget?.size.rows ?? 2;
				const { col, row } = findNextAvailablePosition(elements, colspan, rowspan);

				return [...elements, { type: 'widget', id: widgetId, col, row, colspan, rowspan }];
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
