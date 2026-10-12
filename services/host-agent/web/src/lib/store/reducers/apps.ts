// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Apps slice - the installed-app table, keyed by catalog id.
 *
 * Replaces `stores/apps.ts`. The map-plus-order shape is deliberate: every read
 * in the app is a lookup by catalog id (the tile, the modal, the click handler),
 * and the three of them that used to `array.find` the same list now share one
 * answer. `order` keeps the snapshot's ordering so the grid does not reshuffle
 * when nothing moved.
 */

import type { App, HomeData } from '$lib/types';
import {
	APPS_READ_FAILED,
	APPS_STATUS_PATCHED,
	APPS_UPSERTED,
	HOME_SNAPSHOT
} from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

export interface AppsState {
	byId: Record<string, App>;
	/** Catalog ids in snapshot order: the order the grid renders in. */
	order: string[];
	/** True until the first snapshot lands. */
	loading: boolean;
	error: string | null;
	/** Wall clock of the last accepted snapshot. 0 means none yet. */
	snapshotAt: number;
}

export const initialApps: AppsState = {
	byId: {},
	order: [],
	loading: true,
	error: null,
	snapshotAt: 0
};

export function apps(state = initialApps, action: Action): AppsState {
	switch (action.type) {
		case HOME_SNAPSHOT:
			return fromSnapshot(readPayload<HomeData>(action));
		case APPS_UPSERTED:
			return upsert(state, readPayload<App>(action));
		case APPS_STATUS_PATCHED: {
			const { catalogId, status } = readPayload<{ catalogId: string; status: App['status'] }>(action);
			const existing = state.byId[catalogId];
			if (!existing) return state;
			return { ...state, byId: { ...state.byId, [catalogId]: { ...existing, status } } };
		}
		case APPS_READ_FAILED:
			return { ...state, loading: false, error: readPayload<{ message: string }>(action).message };
		default:
			return state;
	}
}

function fromSnapshot(data: HomeData): AppsState {
	const byId: Record<string, App> = {};
	const order: string[] = [];
	for (const app of data.apps) {
		byId[app.catalog_id] = app;
		order.push(app.catalog_id);
	}
	return { byId, order, loading: false, error: null, snapshotAt: Date.now() };
}

function upsert(state: AppsState, entry: App): AppsState {
	const known = state.byId[entry.catalog_id] !== undefined;
	return {
		...state,
		byId: { ...state.byId, [entry.catalog_id]: entry },
		order: known ? state.order : [...state.order, entry.catalog_id],
		loading: false,
		error: null
	};
}

/**
 * Does this app get a tile? System apps never do; neither does a headless app,
 * which is installed and reconciled but has no page to open, so a tile would only
 * be a broken link. An app mid-uninstall is already gone as far as the grid is
 * concerned. Exported pure so the rule is testable without a store round trip.
 */
export function isGridApp(app: App): boolean {
	return !app.is_system && !app.headless && app.status !== 'uninstalling';
}

/** Status of every app, as the toast transition detector wants it. */
export function statusMap(state: AppsState): Map<string, string> {
	const map = new Map<string, string>();
	for (const [id, app] of Object.entries(state.byId)) map.set(id, app.status);
	return map;
}
