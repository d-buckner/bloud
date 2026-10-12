// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The kernel: what survives a reducer swap, and what the middleware adds.
 *
 * The first case is the hot-reload claim at the size it can actually be verified:
 * state is not rebuilt from scratch when the reducers change, and replaying the
 * last snapshot through the new ones lands on the same state a cold run would
 * have produced. That is the whole argument for keeping the store on `globalThis`,
 * and it is either true or it is not.
 */

import { describe, it, expect, vi } from 'vitest';
import { createRootStore } from '../kernel';
import { rootReducer } from '../reducers';
import { homeSnapshot, toastDismissed, toastPushed } from '../actions';
import type { App, HomeData } from '$lib/types';

function app(catalog_id: string, status: App['status']): HomeData['apps'][number] {
	return {
		id: 1,
		catalog_id,
		display_name: catalog_id,
		version: '1',
		status,
		is_system: false,
		installed_at: '2026-01-01',
		updated_at: '2026-01-01',
		x: null,
		y: null,
		w: 1,
		h: 1
	};
}

function home(...apps: HomeData['apps']): HomeData {
	return { apps, widgets: [] };
}

describe('reducer swap', () => {
	it('keeps the state a swap did not touch', () => {
		const store = createRootStore();
		store.dispatch(homeSnapshot(home(app('jellyfin', 'running'))));
		const before = store.getState();

		store.replaceReducer(rootReducer);
		const after = store.getState();

		expect(after.apps.byId.jellyfin).toEqual(before.apps.byId.jellyfin);
	});

	it('re-deriving the last snapshot reproduces the cold-run state exactly', () => {
		const payload = home(app('jellyfin', 'running'), app('navidrome', 'installing'));

		const cold = createRootStore();
		cold.dispatch(homeSnapshot(payload));

		const swapped = createRootStore();
		swapped.dispatch(homeSnapshot(home(app('jellyfin', 'installing'))));
		swapped.replaceReducer(rootReducer);
		swapped.dispatch(homeSnapshot(payload));

		// Compared over the slices a snapshot produces. Toasts are excluded on
		// purpose: a toast is a *transition*, so a store that arrived here by a
		// different route legitimately holds a different one. Comparing them
		// would assert that history is reproducible, which is not the claim.
		expect(serverSlices(swapped.getState())).toEqual(serverSlices(cold.getState()));
	});
});

function serverSlices(state: ReturnType<typeof rootReducer>) {
	return {
		apps: { ...state.apps, snapshotAt: 0 },
		layout: state.layout,
		launchers: state.launchers,
		progress: state.progress
	};
}

describe('toast middleware', () => {
	it('raises nothing on the first snapshot: there is no transition to report', () => {
		const store = createRootStore();
		store.dispatch(homeSnapshot(home(app('jellyfin', 'running'))));
		expect(store.getState().toasts.items).toHaveLength(0);
	});

	it('raises one when a snapshot moves an app from installing to running', () => {
		const store = createRootStore();
		store.dispatch(homeSnapshot(home(app('jellyfin', 'installing'))));
		store.dispatch(homeSnapshot(home(app('jellyfin', 'running'))));

		const items = store.getState().toasts.items;
		expect(items).toHaveLength(1);
		expect(items[0]).toMatchObject({ tone: 'success', message: 'jellyfin is ready' });
	});

	it('assigns distinct ids, which a module counter would not after a reload', () => {
		const store = createRootStore();
		store.dispatch(toastPushed({ tone: 'info', message: 'one' }));
		store.dispatch(toastPushed({ tone: 'info', message: 'two' }));
		const ids = store.getState().toasts.items.map((t) => t.id);
		expect(new Set(ids).size).toBe(2);
	});

	it('refuses an action no reducer claims, instead of silently changing nothing', () => {
		const store = createRootStore();
		const errors = vi.spyOn(console, 'error').mockImplementation(() => undefined);
		const before = store.getState();

		store.dispatch({ type: 'apss/upserted', payload: {} });

		expect(store.getState()).toBe(before);
		expect(errors).toHaveBeenCalledWith(expect.stringContaining('apss/upserted'));
		errors.mockRestore();
	});

	it('drops a toast when it is dismissed', () => {
		const store = createRootStore();
		store.dispatch(toastPushed({ tone: 'info', message: 'one' }));
		const id = store.getState().toasts.items[0].id;
		store.dispatch(toastDismissed({ id }));
		expect(store.getState().toasts.items).toHaveLength(0);
	});
});
