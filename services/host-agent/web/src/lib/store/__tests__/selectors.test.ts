// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The selectors' identity contract.
 *
 * A component re-renders when the value it selected changes. For a selector that
 * builds an array that means "did I rebuild the array", and rebuilding it on every
 * store notification means the grid re-renders on every SSE tick of an unrelated
 * app. These tests pin the identity, because a selector that is quietly
 * unmemoized still returns the right answer; it just does the work every time.
 */

import { describe, it, expect } from 'vitest';
import { createStore } from 'redux';
import { rootReducer } from '../reducers';
import { appView, gridApps, homeSummary } from '../selectors';
import { appsUpserted, commandStarted, homeSnapshot } from '../actions';
import type { HomeData } from '$lib/types';

function home(...ids: string[]): HomeData {
	return {
		apps: ids.map((catalog_id, i) => ({
			id: i + 1,
			catalog_id,
			display_name: catalog_id,
			version: '1',
			status: 'running' as const,
			is_system: false,
			installed_at: '2026-01-01',
			updated_at: '2026-01-01',
			x: null,
			y: null,
			w: 1,
			h: 1
		})),
		widgets: []
	};
}

describe('selector identity', () => {
	it('returns the same array while the apps slice is untouched', () => {
		const store = createStore(rootReducer);
		store.dispatch(homeSnapshot(home('jellyfin', 'navidrome')));
		const first = gridApps(store.getState());

		// An unrelated action: the commands slice changes, apps does not.
		store.dispatch(commandStarted({ key: 'install:immich', kind: 'request', at: 1 }));

		expect(gridApps(store.getState())).toBe(first);
	});

	it('returns a new array once the apps slice changes', () => {
		const store = createStore(rootReducer);
		store.dispatch(homeSnapshot(home('jellyfin')));
		const first = gridApps(store.getState());

		store.dispatch(
			appsUpserted({
				id: 2,
				catalog_id: 'immich',
				display_name: 'Immich',
				version: '1',
				status: 'running',
				is_system: false,
				installed_at: '2026-01-01',
				updated_at: '2026-01-01'
			})
		);

		expect(gridApps(store.getState())).not.toBe(first);
		expect(gridApps(store.getState())).toHaveLength(2);
	});

	it('caches a per-app view against that app, not against the whole store', () => {
		const store = createStore(rootReducer);
		store.dispatch(homeSnapshot(home('jellyfin', 'navidrome')));
		const jellyfin = appView(store.getState(), 'jellyfin');

		// Navidrome's record is replaced; Jellyfin's is the same object.
		store.dispatch(
			appsUpserted({
				id: 2,
				catalog_id: 'navidrome',
				display_name: 'Navidrome',
				version: '2',
				status: 'running',
				is_system: false,
				installed_at: '2026-01-01',
				updated_at: '2026-01-01'
			})
		);

		expect(appView(store.getState(), 'jellyfin')).toBe(jellyfin);
	});

	it('rebuilds the summary only when one of the three things it counts changes', () => {
		const store = createStore(rootReducer);
		store.dispatch(homeSnapshot(home('jellyfin')));
		const first = homeSummary(store.getState());
		expect(first).toMatchObject({ apps: 1, empty: false });

		store.dispatch(commandStarted({ key: 'install:immich', kind: 'request', at: 1 }));
		expect(homeSummary(store.getState())).toBe(first);
	});
});
