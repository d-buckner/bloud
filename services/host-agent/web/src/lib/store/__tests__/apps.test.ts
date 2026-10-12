// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The apps slice, and the tile rule it shares with the layout slice.
 *
 * The two halves are tested together on purpose: the count in the page subtitle
 * and the tiles on the grid come from different slices, and they disagree only
 * if this rule drifts between them.
 */

import { describe, it, expect } from 'vitest';
import { apps, initialApps, isGridApp } from '../reducers/apps';
import { layout, initialLayout } from '../reducers/layout';
import { homeSnapshot } from '../actions';
import type { App, AppStatus, HomeData } from '$lib/types';

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

function home(apps: HomeData['apps'], launchers: HomeData['launchers'] = []): HomeData {
	return { apps, widgets: [], launchers };
}

function homeApp(overrides: Partial<App> & { catalog_id: string }): HomeData['apps'][number] {
	return { ...app(overrides), x: null, y: null, w: 1, h: 1 };
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

describe('apps slice', () => {
	it('keys the snapshot by catalog id and keeps its order', () => {
		const state = apps(
			initialApps,
			homeSnapshot(home([homeApp({ catalog_id: 'jellyfin' }), homeApp({ catalog_id: 'navidrome' })]))
		);
		expect(state.order).toEqual(['jellyfin', 'navidrome']);
		expect(state.byId.navidrome.display_name).toBe('navidrome');
		expect(state.loading).toBe(false);
	});

	it('keeps a headless app in the slice, so its status still flows', () => {
		const state = apps(
			initialApps,
			homeSnapshot(home([homeApp({ catalog_id: 'affine-mcp', headless: true, status: 'error' })]))
		);
		expect(Object.keys(state.byId)).toHaveLength(1);
		expect(isGridApp(state.byId['affine-mcp'])).toBe(false);
	});

	it('replaces rather than merges: an app the snapshot dropped is gone', () => {
		const withBoth = apps(
			initialApps,
			homeSnapshot(home([homeApp({ catalog_id: 'jellyfin' }), homeApp({ catalog_id: 'navidrome' })]))
		);
		const after = apps(withBoth, homeSnapshot(home([homeApp({ catalog_id: 'jellyfin' })])));
		expect(after.byId.navidrome).toBeUndefined();
	});

	it('records a status patch without touching the rest', () => {
		const base = apps(initialApps, homeSnapshot(home([homeApp({ catalog_id: 'jellyfin' })])));
		const patched = apps(base, {
			type: 'apps/statusPatched',
			payload: { catalogId: 'jellyfin', status: 'uninstalling' as AppStatus }
		});
		expect(patched.byId.jellyfin.status).toBe('uninstalling');
	});
});

describe('layout tiles', () => {
	function appTileIds(state: ReturnType<typeof layout>) {
		return state.elements.filter((el) => el.type === 'app').map((el) => el.id);
	}

	it('builds no tile for a headless app in the home snapshot', () => {
		const state = layout(
			initialLayout,
			homeSnapshot(
				home([
					homeApp({ catalog_id: 'jellyfin' }),
					homeApp({ catalog_id: 'affine-mcp', headless: true })
				])
			)
		);
		expect(appTileIds(state)).toEqual(['jellyfin']);
	});

	it('still builds no tile when a headless app carries a stored position', () => {
		const state = layout(
			initialLayout,
			homeSnapshot(home([{ ...homeApp({ catalog_id: 'affine-mcp', headless: true }), x: 0, y: 0, w: 2, h: 2 }]))
		);
		expect(appTileIds(state)).toEqual([]);
	});
});
