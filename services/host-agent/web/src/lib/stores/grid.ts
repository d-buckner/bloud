// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Grid store - In-memory source of truth for grid element positions.
 *
 * Positions are owned by the server. This store is populated by the home
 * snapshot (SSE, or the adaptive poller fallback) and updated locally only for
 * widget add/remove before the next PUT /api/user/layout confirms the change.
 */

import { writable, derived } from 'svelte/store';
import { getWidgetById } from '$lib/widgets/registry';
import type { GridElement, HomeData } from '$lib/types';

export type { GridElement };

/** App tiles are uniform: one grid cell, not resizable. */
const APP_SIZE = 1;

function createGridStore() {
	const { subscribe, set, update } = writable<GridElement[]>([]);

	return {
		subscribe,

		/** Replace all elements from a home endpoint response. */
		setFromHome(data: HomeData): void {
			const elements: GridElement[] = [];
			for (const app of data.apps) {
				if (app.is_system) continue;
				elements.push({
					type: 'app',
					id: app.catalog_id,
					x: app.x,
					y: app.y,
					w: app.w || APP_SIZE,
					h: app.h || APP_SIZE,
				});
			}
			for (const widget of data.widgets) {
				const def = getWidgetById(widget.id);
				if (!def) continue;
				// The registry size is the default; the stored size is the
				// user's own resize and must win, or the next snapshot would
				// undo it.
				elements.push({
					type: 'widget',
					id: widget.id,
					x: widget.x,
					y: widget.y,
					w: Math.max(widget.w || 0, def.size.cols),
					h: Math.max(widget.h || 0, def.size.rows),
				});
			}
			set(elements);
		},

		removeWidget(widgetId: string): void {
			update((elements) => elements.filter((el) => !(el.type === 'widget' && el.id === widgetId)));
		},

		/**
		 * Add an app element with a null position so GridStack auto-places it.
		 * Used when an install 202 carries the new app record before the next
		 * snapshot arrives.
		 */
		addApp(appId: string): void {
			update((elements) => {
				if (elements.some((el) => el.type === 'app' && el.id === appId)) return elements;
				return [
					...elements,
					{ type: 'app', id: appId, x: null, y: null, w: APP_SIZE, h: APP_SIZE },
				];
			});
		},

		/** Turn a widget on at its registry default size, or off. */
		toggleWidget(widgetId: string): void {
			const def = getWidgetById(widgetId);
			if (!def) return;
			update((elements) => {
				if (elements.some((el) => el.type === 'widget' && el.id === widgetId)) {
					return elements.filter((el) => !(el.type === 'widget' && el.id === widgetId));
				}
				return [
					...elements,
					{
						type: 'widget',
						id: widgetId,
						x: null,
						y: null,
						w: def.size.cols,
						h: def.size.rows,
					},
				];
			});
		},
	};
}

export const gridElements = createGridStore();

export const enabledWidgetIds = derived(gridElements, ($elements) =>
	$elements.filter((el) => el.type === 'widget').map((el) => el.id)
);
