// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The developer graph poll.
 *
 * This is the timer `developer/+page.svelte` used to own: a 500ms `setInterval`
 * with an `inflight` boolean, a `document.hidden` check, and a cleanup closure,
 * all of it invisible to everything else on the page. The interval itself was
 * never the problem, and the plan keeps it: the developer view is a live view of
 * a reconciler, and a poll is an honest way to watch one. What was the problem is
 * that the *decision* of when to read lived in a component, so nothing else
 * could see that a read was happening, could not show its error, and could not
 * stop it.
 *
 * So the cadence moved here, next to the other timers, and it is now gated on
 * two things the component could not both know:
 *
 *  - the route is mounted. `startGraphPoll` runs from the page's `onMount` and
 *    `stopGraphPoll` from its teardown, so navigating away really does stop it.
 *  - the tab is visible. A backgrounded tab gets its timers throttled and has
 *    nothing to show, so polling it is spending requests on nobody.
 */

import { fetchDeveloperGraph } from '$lib/clients/developerClient';
import { dispatch } from '../kernel';
import { graphFailed, graphLoaded } from '../actions';

/** Half a second: fast enough to watch an install, slow enough to be cheap. */
export const GRAPH_POLL_MS = 500;

let timer: ReturnType<typeof setTimeout> | null = null;
let running = false;
let inflight = false;
let onVisibility: (() => void) | null = null;

/** Start polling. Idempotent: a second call does not start a second loop. */
export function startGraphPoll(): void {
	if (running) return;
	running = true;
	onVisibility = () => {
		// Coming back is an immediate read, not a wait for the next tick: the
		// canvas is about to be looked at, and it has been stale this whole time.
		if (document.visibilityState === 'visible') void tick();
	};
	document.addEventListener('visibilitychange', onVisibility);
	void tick();
}

export function stopGraphPoll(): void {
	running = false;
	if (timer !== null) {
		clearTimeout(timer);
		timer = null;
	}
	if (onVisibility) {
		document.removeEventListener('visibilitychange', onVisibility);
		onVisibility = null;
	}
}

/** Is a loop running? For tests and for the one-mount guard below. */
export function isPolling(): boolean {
	return running;
}

async function tick(): Promise<void> {
	if (!running || inflight) return;
	if (document.visibilityState !== 'visible') {
		schedule();
		return;
	}
	inflight = true;
	try {
		dispatch(graphLoaded(await fetchDeveloperGraph(), Date.now()));
	} catch (err) {
		dispatch(graphFailed(messageOf(err)));
	} finally {
		inflight = false;
	}
	schedule();
}

/**
 * `setTimeout` rather than `setInterval`, for the same reason the home poll uses
 * it: a slow response plus a fixed interval stacks requests, and the inflight
 * guard would then silently drop ticks instead of spacing them out.
 */
function schedule(): void {
	if (!running) return;
	timer = setTimeout(() => void tick(), GRAPH_POLL_MS);
}

function messageOf(err: unknown): string {
	if (err instanceof Error && err.message) return err.message;
	if (err && typeof err === 'object' && 'message' in err) return String((err as { message: unknown }).message);
	return 'Failed to load developer graph';
}
