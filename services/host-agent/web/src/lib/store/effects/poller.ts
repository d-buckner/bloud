// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The poll effect - the fallback read of the home snapshot.
 *
 * It runs only while the machine says `polling`, and it stops the moment the
 * stream proves itself again. The interval is adaptive for the same reason the
 * old poller was: an install in flight wants a fast refresh, an idle dashboard
 * does not need one.
 *
 * What changed is ownership. `clients/poller.ts` took a callback from whoever
 * started it, so its lifetime was a side effect of the last caller; this module
 * dispatches, and the connection slice decides whether it should be running.
 */

import { dispatch, getState } from '../kernel';
import { connectionEvent, homeSnapshot } from '../actions';
import type { HomeData } from '$lib/types';

const HOME_URL = '/api/user/home';
const IDLE_INTERVAL_MS = 10_000;
const ACTIVE_INTERVAL_MS = 2_000;

/** Statuses that mean something is still happening on the box. */
const TRANSITIONING = new Set(['installing', 'starting', 'uninstalling']);

let timer: ReturnType<typeof setTimeout> | null = null;
let running = false;

export function startPolling(): void {
	if (running) return;
	running = true;
	void tick();
}

export function stopPolling(): void {
	running = false;
	if (timer !== null) {
		clearTimeout(timer);
		timer = null;
	}
}

async function tick(): Promise<void> {
	if (!running) return;
	try {
		const res = await fetch(HOME_URL, { headers: { Accept: 'application/json' } });
		if (!res.ok) throw new Error(`home poll failed: ${res.status}`);
		dispatch(connectionEvent({ type: 'poll.ok' }));
		dispatch(homeSnapshot((await res.json()) as HomeData));
	} catch {
		// Nothing is dispatched: a failed poll does not change which source the
		// machine treats as authoritative. The next tick is the retry.
	}
	schedule();
}

function schedule(): void {
	if (!running) return;
	timer = setTimeout(() => void tick(), nextIntervalMs());
}

function nextIntervalMs(): number {
	const { byId } = getState().apps;
	return Object.values(byId).some((app) => TRANSITIONING.has(app.status))
		? ACTIVE_INTERVAL_MS
		: IDLE_INTERVAL_MS;
}
