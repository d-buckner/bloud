// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Selector memoization, in the twenty lines this needs.
 *
 * A selector that builds an array or an object returns a new reference on every
 * call, so every store notification gives every consumer a "new" value and
 * re-renders a grid that did not change. The store notifies on every action,
 * including every SSE tick during an install, so at `ACTIVE_INTERVAL_MS` and
 * node-event rates that is real work for no reason.
 *
 * The fix is the same idea reselect sells: cache the built value and recompute
 * only when the slices the selector actually read have changed identity. The
 * reducers already preserve a slice's identity when they do not touch it, which
 * is what makes an identity comparison the right question.
 *
 * It is here rather than a dependency because the issue's constraint is to
 * minimize bloat, and the two caches below are the whole requirement: one entry
 * for the plain selectors, one keyed by argument for the parameterized ones.
 */

/** Compare the dependency list a selector declared against the previous one. */
function same(a: readonly unknown[], b: readonly unknown[]): boolean {
	if (a.length !== b.length) return false;
	return a.every((value, i) => Object.is(value, b[i]));
}

/**
 * Memoize a selector over the slices it reads.
 *
 * `read` returns the dependencies and `build` does the work; keeping them split
 * is what stops the cache from comparing the value it is about to rebuild.
 */
export function memo<T, S>(read: (state: S) => unknown[], build: (state: S) => T): (state: S) => T {
	let lastDeps: readonly unknown[] = [];
	let value: T;
	let ready = false;
	return (state) => {
		const deps = read(state);
		if (ready && same(deps, lastDeps)) return value;
		lastDeps = deps;
		value = build(state);
		ready = true;
		return value;
	};
}

/**
 * Memoize a selector that takes an argument, one cache entry per argument.
 *
 * The per-argument cache is what makes a grid of tiles cheap: each tile's view
 * is cached against its own app and progress record, so one app changing does
 * not rebuild the other nine.
 */
export function memoBy<A, T, S>(
	read: (state: S, arg: A) => unknown[],
	build: (state: S, arg: A) => T
): (state: S, arg: A) => T {
	const cache = new Map<A, { deps: readonly unknown[]; value: T }>();
	return (state, arg) => {
		const deps = read(state, arg);
		const hit = cache.get(arg);
		if (hit && same(hit.deps, deps)) return hit.value;
		const value = build(state, arg);
		cache.set(arg, { deps, value });
		return value;
	};
}
