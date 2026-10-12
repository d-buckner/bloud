// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The graph slice: what a poll that fails must not do.
 *
 * The developer view is a live picture of a reconciler. A single failed request
 * in a 500ms poll is routine, and the old page handled it by setting `error`
 * while leaving the last layout on screen, which was right about the canvas and
 * silent about the fact that the picture had stopped moving. The slice keeps the
 * payload and names the staleness, so the page can say both.
 */

import { describe, it, expect } from 'vitest';
import { graph, initialGraph, isStale } from '../reducers/graph';
import { graphFailed, graphLoaded, graphReset } from '../actions';
import type { DeveloperGraph } from '$lib/clients/developerClient';

function graphWith(ids: string[]): DeveloperGraph {
	return {
		nodes: ids.map((id) => ({
			id,
			displayName: id,
			status: 'RUNNING',
			isSystem: false,
			nodeType: 'app'
		})),
		edges: []
	};
}

describe('graph slice', () => {
	it('starts empty and not loaded', () => {
		expect(graph(undefined, { type: '@@init' })).toEqual(initialGraph);
		expect(initialGraph.loaded).toBe(false);
	});

	it('replaces the payload rather than merging it', () => {
		let state = graph(initialGraph, graphLoaded(graphWith(['jellyfin']), 1));
		state = graph(state, graphLoaded(graphWith(['immich']), 2));

		expect(state.payload?.nodes.map((n) => n.id)).toEqual(['immich']);
		expect(state.lastAt).toBe(2);
	});

	it('keeps the last good payload when a poll fails, and says it is stale', () => {
		const good = graph(initialGraph, graphLoaded(graphWith(['jellyfin']), 1));
		const failed = graph(good, graphFailed('gateway timeout'));

		expect(failed.payload).toBe(good.payload);
		expect(failed.loaded).toBe(true);
		expect(failed.error).toBe('gateway timeout');
		expect(isStale(failed)).toBe(true);
	});

	it('does not call a first failure stale: there is nothing stale to show', () => {
		const failed = graph(initialGraph, graphFailed('gateway timeout'));
		expect(failed.loaded).toBe(false);
		expect(isStale(failed)).toBe(false);
	});

	it('clears the error on the next good read', () => {
		let state = graph(initialGraph, graphLoaded(graphWith(['a']), 1));
		state = graph(state, graphFailed('boom'));
		state = graph(state, graphLoaded(graphWith(['a']), 2));

		expect(state.error).toBe('');
		expect(isStale(state)).toBe(false);
	});

	it('resets to nothing', () => {
		const state = graph(graph(initialGraph, graphLoaded(graphWith(['a']), 1)), graphReset());
		expect(state).toEqual(initialGraph);
	});
});
