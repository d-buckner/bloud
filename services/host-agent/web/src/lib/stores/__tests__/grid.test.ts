// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { gridElements } from '../grid';
import type { Launcher } from '$lib/types';

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

describe('grid launcher tiles', () => {
	beforeEach(() => {
		gridElements.setFromHome({ apps: [], widgets: [] });
	});

	it('builds a launcher tile from the home snapshot', () => {
		gridElements.setFromHome({
			apps: [],
			widgets: [],
			launchers: [launcher({ id: 'l1', name: 'Photos' })]
		});

		expect(get(gridElements)).toEqual([
			{ type: 'launcher', id: 'l1', x: null, y: null, w: 1, h: 1 }
		]);
	});

	it('keeps launchers and app tiles together in one element list', () => {
		gridElements.setFromHome({
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
			widgets: [],
			launchers: [launcher({ id: 'l1' })]
		});

		const types = get(gridElements).map((el) => el.type);
		expect(types).toEqual(['app', 'launcher']);
	});

	it('builds no launcher tile when the snapshot omits launchers', () => {
		gridElements.setFromHome({ apps: [], widgets: [] });
		expect(get(gridElements)).toEqual([]);
	});
});
