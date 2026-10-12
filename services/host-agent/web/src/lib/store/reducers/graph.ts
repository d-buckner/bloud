// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Graph slice - what the developer page knows about the reconciler.
 *
 * This is the third thing #309 names by hand ("the app catalog, developer graph
 * data, install progress"), and the last one that was still a component's
 * private business: six `$state` fields, a 500ms interval, and an `inflight`
 * boolean inside `developer/+page.svelte`, none of it reachable from anywhere
 * else and none of it testable.
 *
 * What lives here is the *read*: the payload, whether it has landed, and what
 * failed. What deliberately does not live here is the xyflow projection.
 * `layoutGraph` runs dagre, and dagre is a runtime import of
 * `$lib/timeline/graphLayout`; a reducer that called it would move a layout
 * library from the developer route's chunk into the store's, and every visitor
 * on the home page would download a graph layouter they will never see. The
 * projection is a rendering concern of one route and stays there.
 */

import { GRAPH_FAILED, GRAPH_LOADED, GRAPH_RESET } from '../actions';
import type { Action } from '../actions';
import type { DeveloperGraph } from '$lib/clients/developerClient';
import { readPayload } from './readPayload';

export interface GraphState {
	/** The last payload that arrived, or null before the first one does. */
	payload: DeveloperGraph | null;
	/** Empty while things are fine. A failed read keeps the last payload. */
	error: string;
	/** True once any read has succeeded, which is what the canvas mounts on. */
	loaded: boolean;
	/** When the last successful read landed. 0 means never. */
	lastAt: number;
}

export const initialGraph: GraphState = {
	payload: null,
	error: '',
	loaded: false,
	lastAt: 0
};

export function graph(state = initialGraph, action: Action): GraphState {
	switch (action.type) {
		case GRAPH_LOADED: {
			const { payload, at } = readPayload<{ payload: DeveloperGraph; at: number }>(action);
			// A successful read clears the error: the way this slice says "back to
			// normal" is by reading successfully, not by a separate reset.
			return { payload, error: '', loaded: true, lastAt: at };
		}
		case GRAPH_FAILED: {
			const { error } = readPayload<{ error: string }>(action);
			// The payload survives on purpose. A poll that fails once should not
			// blank a canvas that is showing real state; it should say it is stale.
			return { ...state, error };
		}
		case GRAPH_RESET:
			return initialGraph;
		default:
			return state;
	}
}

/** Is the page looking at something it could not refresh? */
export function isStale(state: GraphState): boolean {
	return state.error !== '' && state.loaded;
}
