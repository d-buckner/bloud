// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The layout slice: one element list, rebuilt from the snapshot, edited locally
 * between an action and the snapshot that confirms it.
 */

import { describe, it, expect } from 'vitest';
import { layout, initialLayout } from '../reducers/layout';
import { homeSnapshot, layoutElementAdded, layoutElementRemoved } from '../actions';
import type { GridElement, HomeData, Launcher } from '$lib/types';

function launcher(overrides: Partial<Launcher> & { id: string }): Launcher {
	return {
		name: overrides.id,
		url: 'https://example.com',
		icon: '',
		x: null,
		y: null,
		w: 1,
		h: 1,
		...overrides
	};
}

function home(data: Partial<HomeData>): HomeData {
	return { apps: [], widgets: [], launchers: [], ...data };
}

function apply(...actions: ReturnType<typeof homeSnapshot>[]): ReturnType<typeof layout> {
	return actions.reduce<ReturnType<typeof layout>>((state, action) => layout(state, action), initialLayout);
}

describe('layout from a snapshot', () => {
	it('builds a launcher tile', () => {
		const state = apply(homeSnapshot(home({ launchers: [launcher({ id: 'l1', name: 'Photos' })] })));
		const expected: GridElement[] = [{ type: 'launcher', id: 'l1', x: null, y: null, w: 1, h: 1 }];
		expect(state.elements).toEqual(expected);
	});

	it('keeps launchers and app tiles in one element list, apps first', () => {
		const state = apply(
			homeSnapshot(
				home({
					apps: [
						{
							id: 1,
							catalog_id: 'jellyfin',
							display_name: 'Jellyfin',
							version: '1',
							status: 'running',
							is_system: false,
							installed_at: '2026-01-01',
							updated_at: '2026-01-01',
							x: null,
							y: null,
							w: 1,
							h: 1
						}
					],
					launchers: [launcher({ id: 'l1' })]
				})
			)
		);
		expect(state.elements.map((el) => el.type)).toEqual(['app', 'launcher']);
	});

	it('builds no tile at all when the snapshot is empty', () => {
		expect(apply(homeSnapshot(home({}))).elements).toEqual([]);
	});
});

describe('local layout edits', () => {
	it('adds an element once, so a double click cannot double the tile', () => {
		const element: GridElement = { type: 'app', id: 'jellyfin', x: null, y: null, w: 1, h: 1 };
		let state = layout(initialLayout, layoutElementAdded(element));
		state = layout(state, layoutElementAdded(element));
		expect(state.elements).toHaveLength(1);
	});

	it('removes by type and id, not by id alone', () => {
		let state = layout(initialLayout, layoutElementAdded({ type: 'widget', id: 'clock', x: 0, y: 0, w: 2, h: 2 }));
		state = layout(state, layoutElementRemoved({ type: 'app', id: 'clock' }));
		expect(state.elements).toHaveLength(1);
		state = layout(state, layoutElementRemoved({ type: 'widget', id: 'clock' }));
		expect(state.elements).toHaveLength(0);
	});
});
