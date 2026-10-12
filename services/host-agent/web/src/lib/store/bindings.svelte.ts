// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The binding - how a component reads the store, in runes.
 *
 * There is exactly one way: `select(selector)`, then `.current`. It is not a
 * `svelte/store` wrapper, and that is the point. A `readable` shim would let the
 * old `$store` idiom keep writing new code, and a shim that nothing has to delete
 * is a second way to do the same thing forever. One function, one call shape, and
 * a grep that proves it.
 *
 * Under the hood: a version counter bumped on every store notification, read
 * inside the getter. The state object itself is kept plain rather than made
 * reactive, so Svelte is not deep-proxying a whole app catalog on every render;
 * the counter is the only signal, and the selector recomputes from the plain
 * snapshot.
 */

import { store } from './kernel';
import type { RootState } from './reducers';

let version = $state(0);
let snapshot: RootState = store.getState();

store.subscribe(() => {
	snapshot = store.getState();
	version += 1;
});

/** Reading the counter inside a getter is what subscribes that getter. */
function tracked(): number {
	return version;
}

export interface Readable<T> {
	readonly current: T;
}

/** A live view over one part of the store. Read it with `.current`. */
export function select<T>(selector: (state: RootState) => T): Readable<T> {
	return {
		get current(): T {
			tracked();
			return selector(snapshot);
		}
	};
}

/** Subscribe to a slice without runes: for effects and tests. */
export function watch<T>(selector: (state: RootState) => T, onChange: (value: T) => void): () => void {
	let previous = selector(store.getState());
	return store.subscribe(() => {
		const next = selector(store.getState());
		if (next === previous) return;
		previous = next;
		onChange(next);
	});
}
