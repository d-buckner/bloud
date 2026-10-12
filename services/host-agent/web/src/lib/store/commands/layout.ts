// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Layout commands - where things sit, and what is on the grid.
 *
 * Positions are server-owned, so every local edit here is provisional until the
 * PUT lands. `saveLayout` used to be a bare `fetch` in `GridStackGrid.svelte`
 * with a `catch {}` that called the failure "best-effort": a layout the server
 * never took would look saved forever, and the next snapshot would quietly undo
 * it. It is a command now, so the failure is at least a fact in the store.
 */

import { put } from '$lib/clients/httpClient';
import type { GridElement } from '$lib/types';
import { runCommand } from '../runner';
import type { CommandResult } from '../runner';
import { commandKey } from '../reducers/commands';
import { layoutElementAdded, layoutElementRemoved } from '../actions';
import { dispatch, getState } from '../kernel';
import { widgetElement } from '../reducers/layout';

const LAYOUT_KEY = commandKey('layout', 'home');

/** Send the settled layout. The full element list, always, as the API wants it. */
export function saveLayout(elements?: GridElement[]): Promise<CommandResult<null>> {
	const payload = elements ?? getState().layout.elements;
	return runCommand<null>({
		key: LAYOUT_KEY,
		kind: 'request',
		run: async () => {
			await put('/api/user/layout', payload);
			return null;
		}
	});
}

/** Turn a widget on at its registry default size, or off. */
export function toggleWidget(widgetId: string): void {
	const existing = getState().layout.elements.some((el) => el.type === 'widget' && el.id === widgetId);
	if (existing) {
		dispatch(layoutElementRemoved({ type: 'widget', id: widgetId }));
	} else {
		const element = widgetElement(widgetId);
		if (!element) return;
		dispatch(layoutElementAdded(element));
	}
	void saveLayout();
}

/** Drop a widget from the grid, from inside the widget itself. */
export function removeWidget(widgetId: string): void {
	dispatch(layoutElementRemoved({ type: 'widget', id: widgetId }));
	void saveLayout();
}

/** Record where gridstack settled the tiles, then persist it. */
export function setPositions(elements: GridElement[]): void {
	void saveLayout(elements);
}
