// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import { apps, visibleApps, isGridApp } from '../apps';
import { gridElements } from '../grid';
import type { App } from '$lib/types';

function app(overrides: Partial<App> & { catalog_id: string }): App {
	return {
		id: 1,
		display_name: overrides.catalog_id,
		version: '1',
		status: 'running',
		is_system: false,
		installed_at: '2026-01-01',
		updated_at: '2026-01-01',
		...overrides
	};
}

describe('isGridApp', () => {
	it('gives a tile to an app with a UI', () => {
		expect(isGridApp(app({ catalog_id: 'jellyfin' }))).toBe(true);
	});

	it('gives no tile to a headless app', () => {
		expect(isGridApp(app({ catalog_id: 'affine-mcp', headless: true }))).toBe(false);
	});

	it('gives no tile to a system app', () => {
		expect(isGridApp(app({ catalog_id: 'traefik', is_system: true }))).toBe(false);
	});

	it('gives no tile to an app mid-uninstall', () => {
		expect(isGridApp(app({ catalog_id: 'jellyfin', status: 'uninstalling' }))).toBe(false);
	});

	it('gives no tile to a headless app even while it is installing', () => {
		expect(isGridApp(app({ catalog_id: 'affine-mcp', headless: true, status: 'installing' }))).toBe(false);
	});

	it('treats a missing headless field as having a UI', () => {
		expect(isGridApp(app({ catalog_id: 'immich' }))).toBe(true);
	});
});

describe('visibleApps', () => {
	beforeEach(() => {
		apps.set([]);
	});

	it('drops the headless app and keeps every app with a UI', () => {
		apps.set([
			app({ catalog_id: 'jellyfin' }),
			app({ catalog_id: 'affine-mcp', headless: true }),
			app({ catalog_id: 'navidrome' })
		]);
		expect(get(visibleApps).map((a) => a.catalog_id)).toEqual(['jellyfin', 'navidrome']);
	});

	it('keeps the headless app in the underlying store, so its status still flows', () => {
		apps.set([app({ catalog_id: 'affine-mcp', headless: true, status: 'error' })]);
		expect(get(apps)).toHaveLength(1);
		expect(get(visibleApps)).toHaveLength(0);
	});
});

// The tiles themselves come from the grid store, not from visibleApps, so the
// same rule has to hold there or the count and the grid disagree.
describe('grid tiles', () => {
	beforeEach(() => {
		gridElements.setFromHome({ apps: [], widgets: [] });
	});

	function appTileIds() {
		return get(gridElements)
			.filter((el) => el.type === 'app')
			.map((el) => el.id);
	}

	it('builds no tile for a headless app in the home snapshot', () => {
		gridElements.setFromHome({
			apps: [
				{ ...app({ catalog_id: 'jellyfin' }), x: null, y: null, w: 1, h: 1 },
				{ ...app({ catalog_id: 'affine-mcp', headless: true }), x: null, y: null, w: 1, h: 1 }
			],
			widgets: []
		});
		expect(appTileIds()).toEqual(['jellyfin']);
	});

	it('still builds no tile when a headless app carries a stored position', () => {
		gridElements.setFromHome({
			apps: [{ ...app({ catalog_id: 'affine-mcp', headless: true }), x: 0, y: 0, w: 2, h: 2 }],
			widgets: []
		});
		expect(appTileIds()).toEqual([]);
	});
});
