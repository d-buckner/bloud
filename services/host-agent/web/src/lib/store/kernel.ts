// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The kernel - one redux store, one identity for the life of the page.
 *
 * Three jobs beyond `createStore`:
 *
 *  1. A single instance. The store lives on `globalThis` when a dev server is
 *     attached, so a hot reload hands the next module graph the *same* store
 *     instead of a fresh empty one. That is what makes editing a reducer keep
 *     the grid, the open install progress, and the stream alive.
 *  2. `replaceReducer` on accept, plus `resync`: the state survives a swap, but
 *     it was produced by the old reducers, so the last snapshot is replayed
 *     through the new ones. Replaying the last snapshot rather than a history of
 *     events is deliberate: the snapshot is the fence the transport already
 *     provides, so this is one dispatch, not a replay mechanism.
 *  3. Devtools, wired only when the extension exists.
 */

import { applyMiddleware, createStore } from 'redux';
import type { Middleware, MiddlewareAPI, Store, StoreEnhancer } from 'redux';
import type { HomeData } from '$lib/types';
import { HOME_SNAPSHOT, commandsAbandoned, homeSnapshot } from './actions';
import type { Action } from './actions';
import { rootReducer } from './reducers';
import type { RootState } from './reducers';
import { toastTransitions, toastTtl, unknownActionGuard } from './middleware';

/** Where the live store is parked so a hot reload can pick it back up. */
const STORE_KEY = '__BLOUD_STORE__';

/** The last home snapshot the store was given, kept only to re-apply on a swap. */
let lastSnapshot: HomeData | null = null;

type TypedMiddleware =
	(api: MiddlewareAPI) => (next: (action: Action) => unknown) => (action: Action) => unknown;
const asMiddleware = (fn: TypedMiddleware): Middleware => fn as unknown as Middleware;

const rememberSnapshot: Middleware = asMiddleware(() => (next) => (action) => {
	if (action.type === HOME_SNAPSHOT) {
		lastSnapshot = action.payload as HomeData;
	}
	return next(action);
});

export const MIDDLEWARE = [unknownActionGuard, toastTransitions, toastTtl, rememberSnapshot];

interface DevtoolsCompose {
	__REDUX_DEVTOOLS_EXTENSION_COMPOSE__?: (config: { name: string }) => (...m: Middleware[]) => StoreEnhancer;
}

/** The middleware, wrapped in devtools when the browser has the extension. */
function enhancer(): StoreEnhancer {
	const extension = (globalThis as DevtoolsCompose).__REDUX_DEVTOOLS_EXTENSION_COMPOSE__;
	if (typeof window !== 'undefined' && extension) return extension({ name: 'bloud' })(...MIDDLEWARE);
	return applyMiddleware(...MIDDLEWARE);
}

/** Exported for tests: a store with the production wiring and no singleton. */
export function createRootStore(preloaded?: Partial<RootState>): Store<RootState> {
	return createStore(rootReducer, preloaded as RootState | undefined, enhancer());
}

function globalSlot(): Record<string, unknown> {
	return globalThis as unknown as Record<string, unknown>;
}

function hotData(): Record<string, unknown> | null {
	return import.meta.hot ? (import.meta.hot.data as Record<string, unknown>) : null;
}

function resolveStore(): Store<RootState> {
	const slot = globalSlot();
	const carried = slot[STORE_KEY] as Store<RootState> | undefined;
	if (import.meta.hot && carried) {
		// Same store, new reducers: swap them and re-derive what it holds.
		carried.replaceReducer(rootReducer);
		// A module swap keeps the store but not the promises that were driving it.
		// A pending record is now a claim nothing can confirm or drop, so its overlay
		// would pin forever. Forget the set before the snapshot replay repopulates
		// the slices the overlays were projected onto.
		carried.dispatch(commandsAbandoned());
		if (lastSnapshot) carried.dispatch(homeSnapshot(lastSnapshot));
		return carried;
	}
	const fresh = createRootStore();
	slot[STORE_KEY] = fresh;
	const hot = hotData();
	if (hot) hot[STORE_KEY] = fresh;
	return fresh;
}

export const store: Store<RootState> = resolveStore();
if (import.meta.hot) import.meta.hot.accept();

/** Re-apply the last snapshot through the current reducers. Dev-only use. */
export function resync(): void {
	if (lastSnapshot) store.dispatch(homeSnapshot(lastSnapshot));
}

/** The most recent snapshot the store has seen, or null before the first. */
export function lastHomeSnapshot(): HomeData | null {
	return lastSnapshot;
}

/** The one way app code dispatches. */
export function dispatch(action: Action): void {
	store.dispatch(action);
}

export function getState(): RootState {
	return store.getState();
}
