// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Layout slice - what is on the home grid and where.
 *
 * Replaces `stores/grid.ts`. Positions are owned by the server: this slice is
 * rebuilt from the home snapshot and only edited locally for the moment between
 * a user action and the next snapshot. The element list is one array because
 * gridstack needs one array, and splitting apps from widgets from launchers
 * would mean re-joining them at every render.
 */

import type { GridElement, HomeData, HomeWidget, Launcher } from '$lib/types';
import { getWidgetById } from '$lib/widgets/registry';
import {
	HOME_SNAPSHOT,
	LAYOUT_ELEMENT_ADDED,
	LAYOUT_ELEMENT_REMOVED,
	LAYOUT_POSITIONS_SET
} from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

export type { GridElement };

/** App tiles and launcher tiles are uniform: one cell, not resizable. */
const APP_SIZE = 1;

export interface LayoutState {
	elements: GridElement[];
}

export const initialLayout: LayoutState = { elements: [] };

export function layout(state = initialLayout, action: Action): LayoutState {
	switch (action.type) {
		case HOME_SNAPSHOT:
			return { elements: elementsFromHome(readPayload<HomeData>(action)) };
		case LAYOUT_ELEMENT_ADDED: {
			const el = readPayload<GridElement>(action);
			if (state.elements.some((e) => e.type === el.type && e.id === el.id)) return state;
			return { elements: [...state.elements, el] };
		}
		case LAYOUT_ELEMENT_REMOVED: {
			const { type, id } = readPayload<{ type: GridElement['type']; id: string }>(action);
			return { elements: state.elements.filter((e) => !(e.type === type && e.id === id)) };
		}
		case LAYOUT_POSITIONS_SET:
			return { elements: readPayload<GridElement[]>(action) };
		default:
			return state;
	}
}

/** App tiles, skipping system apps and the headless ones with no UI to open. */
function appElements(data: HomeData): GridElement[] {
	const elements: GridElement[] = [];
	for (const app of data.apps) {
		if (app.is_system || app.headless) continue;
		elements.push({
			type: 'app',
			id: app.catalog_id,
			x: app.x,
			y: app.y,
			w: app.w || APP_SIZE,
			h: app.h || APP_SIZE
		});
	}
	return elements;
}

/**
 * Widget tiles. The registry size is the default and the stored size is the
 * user's own resize, which has to win or the next snapshot would undo it.
 */
function widgetElements(widgets: HomeWidget[]): GridElement[] {
	const elements: GridElement[] = [];
	for (const widget of widgets) {
		const def = getWidgetById(widget.id);
		if (!def) continue;
		elements.push({
			type: 'widget',
			id: widget.id,
			x: widget.x,
			y: widget.y,
			w: Math.max(widget.w || 0, def.size.cols),
			h: Math.max(widget.h || 0, def.size.rows)
		});
	}
	return elements;
}

/** Launcher tiles: fixed one-cell like apps, but they open a URL. */
export function launcherElements(launchers: Launcher[]): GridElement[] {
	return launchers.map((launcher) => ({
		type: 'launcher' as const,
		id: launcher.id,
		x: launcher.x,
		y: launcher.y,
		w: launcher.w || APP_SIZE,
		h: launcher.h || APP_SIZE
	}));
}

/** Rebuild the whole element list from one home payload. */
export function elementsFromHome(data: HomeData): GridElement[] {
	return [
		...appElements(data),
		...launcherElements(data.launchers ?? []),
		...widgetElements(data.widgets)
	];
}

/** The tile an install 202 promises exists, before the snapshot confirms it. */
export function appElement(appId: string): GridElement {
	return { type: 'app', id: appId, x: null, y: null, w: APP_SIZE, h: APP_SIZE };
}

/** Turn a widget on at its registry default size, or off. */
export function widgetElement(widgetId: string): GridElement | null {
	const def = getWidgetById(widgetId);
	if (!def) return null;
	return { type: 'widget', id: widgetId, x: null, y: null, w: def.size.cols, h: def.size.rows };
}
