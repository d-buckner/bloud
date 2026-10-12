// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The graph poll's gating, which is the whole reason it moved out of the page.
 *
 * The interval was never the problem. The problem was that only the page knew it
 * existed: nothing else could see a read in flight, could stop it, or could tell
 * a backgrounded tab to stop paying for a picture nobody is looking at. These
 * tests are the parts of that promise the component could not express.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as kernel from '../kernel';
import { graphFailed } from '../actions';
import { GRAPH_POLL_MS, isPolling, startGraphPoll, stopGraphPoll } from '../effects/graphPoll';
import type { DeveloperGraph } from '$lib/clients/developerClient';

const EMPTY: DeveloperGraph = { nodes: [], edges: [] };

let fetched = 0;
let mode: 'ok' | 'fail' = 'ok';

const listeners = new Map<string, () => void>();
let visibility: 'visible' | 'hidden' = 'visible';
const dom = {
	get visibilityState() {
		return visibility;
	},
	addEventListener(type: string, fn: () => void) {
		listeners.set(type, fn);
	},
	removeEventListener(type: string) {
		listeners.delete(type);
	},
	dispatchEvent(event: Event): boolean {
		listeners.get(event.type)?.();
		return true;
	}
} as unknown as Document;

/** Shaped the way `httpClient.request` reads: text plus a content-type header. */
function jsonResponse(body: unknown): Response {
	return {
		ok: true,
		status: 200,
		text: async () => JSON.stringify(body),
		json: async () => body,
		headers: new Headers({ 'content-type': 'application/json' })
	} as Response;
}

function errorResponse(status: number, message: string): Response {
	return {
		ok: false,
		status,
		json: async () => ({ error: message }),
		headers: new Headers()
	} as Response;
}

function setVisibility(state: 'visible' | 'hidden') {
	visibility = state;
}

/**
 * The whole DOM surface this effect touches: a visibility flag and two listener
 * calls. Stubbed rather than pulling in jsdom, because a DOM environment for one
 * test file is a dependency paid forever to exercise four lines, and because the
 * stub says exactly what the effect depends on. If it ever needs more than this,
 * that is worth noticing rather than quietly accommodated.
 */

beforeEach(() => {
	vi.useFakeTimers();
	vi.stubGlobal('document', dom);
	fetched = 0;
	mode = 'ok';
	setVisibility('visible');
	vi.stubGlobal(
		'fetch',
		vi.fn(async () => {
			fetched += 1;
			if (mode === 'fail') return errorResponse(500, 'gateway timeout');
			return jsonResponse(EMPTY);
		})
	);
});

afterEach(() => {
	stopGraphPoll();
	listeners.clear();
	vi.useRealTimers();
	vi.restoreAllMocks();
});

describe('graph poll', () => {
	it('reads once, immediately, on start', () => {
		startGraphPoll();
		expect(fetched).toBe(1);
		expect(isPolling()).toBe(true);
	});

	it('keeps reading on the interval while the tab is visible', async () => {
		startGraphPoll();
		await vi.advanceTimersByTimeAsync(GRAPH_POLL_MS * 3);
		// One immediate plus three ticks. `setTimeout` rather than `setInterval`,
		// so a slow response spaces the next one instead of stacking it.
		expect(fetched).toBe(4);
	});

	it('stops reading while the tab is hidden, and reads at once when it returns', async () => {
		startGraphPoll();
		setVisibility('hidden');
		await vi.advanceTimersByTimeAsync(GRAPH_POLL_MS * 4);
		expect(fetched).toBe(1);

		setVisibility('visible');
		document.dispatchEvent(new Event('visibilitychange'));
		expect(fetched).toBe(2);
	});

	it('stops completely when the route tears down', async () => {
		startGraphPoll();
		stopGraphPoll();
		expect(isPolling()).toBe(false);
		await vi.advanceTimersByTimeAsync(GRAPH_POLL_MS * 4);
		expect(fetched).toBe(1);
	});

	it('never overlaps a read', async () => {
		let release: () => void = () => undefined;
		vi.stubGlobal(
			'fetch',
			vi.fn(async () => {
				fetched += 1;
				await new Promise<void>((resolve) => (release = resolve));
				return jsonResponse(EMPTY);
			})
		);

		startGraphPoll();
		await vi.advanceTimersByTimeAsync(GRAPH_POLL_MS * 10);
		expect(fetched).toBe(1); // still waiting on the first one
		release();
	});

	it('reports a failure to the store instead of swallowing it', async () => {
		const dispatch = vi.spyOn(kernel, 'dispatch').mockImplementation(() => undefined);
		mode = 'fail';

		startGraphPoll();
		await vi.advanceTimersByTimeAsync(0);

		expect(dispatch).toHaveBeenCalledWith(graphFailed(expect.any(String)));
	});

	it('tells the store what landed, with a clock the reducer can trust', async () => {
		const dispatch = vi.spyOn(kernel, 'dispatch').mockImplementation(() => undefined);
		startGraphPoll();
		await vi.advanceTimersByTimeAsync(0);

		// Matched on shape, not on `EMPTY`: the payload is what `json()` parsed,
		// so it is equal to the fixture and never the same object.
		expect(dispatch).toHaveBeenCalledWith({
			type: 'graph/loaded',
			payload: { payload: EMPTY, at: expect.any(Number) }
		});
	});
});
