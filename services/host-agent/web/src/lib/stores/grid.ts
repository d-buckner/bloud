// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Grid store - In-memory source of truth for grid element positions.
 *
 * Positions are owned by the server. This store is populated by the poller
 * (GET /api/user/home) and updated locally only for widget add/remove
 * before the next PUT /api/user/layout confirms the change.
 */

import { writable, derived } from 'svelte/store';
import { getWidgetById, isValidWidgetId } from '$lib/widgets/registry';
import type { GridElement, HomeData } from '$lib/types';

export type { GridElement };

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
					w: app.w || 1,
					h: app.h || 1,
				});
			}
			for (const widget of data.widgets) {
				const def = getWidgetById(widget.id);
				if (!def) continue;
				elements.push({
					type: 'widget',
					id: widget.id,
					x: widget.x,
					y: widget.y,
					w: def.size.cols,
					h: def.size.rows,
				});
			}
			set(elements);
		},

		/** Add a widget with null position so GridStack auto-places it. */
		addWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;
			update((elements) => {
				if (elements.some((el) => el.type === 'widget' && el.id === widgetId)) return elements;
				const widget = getWidgetById(widgetId);
				return [
					...elements,
					{
						type: 'widget',
						id: widgetId,
						x: null,
						y: null,
						w: widget?.size.cols ?? 2,
						h: widget?.size.rows ?? 2,
					},
				];
			});
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
					{
						type: 'app',
						id: appId,
						x: null,
						y: null,
						w: 1,
						h: 1,
					},
				];
			});
		},

		toggleWidget(widgetId: string): void {
			if (!isValidWidgetId(widgetId)) return;
			update((elements) => {
				const exists = elements.some((el) => el.type === 'widget' && el.id === widgetId);
				if (exists) {
					return elements.filter((el) => !(el.type === 'widget' && el.id === widgetId));
				}
				const widget = getWidgetById(widgetId);
				return [
					...elements,
					{
						type: 'widget',
						id: widgetId,
						x: null,
						y: null,
						w: widget?.size.cols ?? 2,
						h: widget?.size.rows ?? 2,
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
