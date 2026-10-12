// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The connection effect - applies the machine's answer to the transport.
 *
 * This is the only place that opens or closes anything, and it never decides
 * whether to: it reads `state.connection.phase` and makes reality match. The
 * backoff lives here because it is a transport detail, not a state transition,
 * and it is driven by the retry counter the machine already keeps rather than a
 * second counter nobody can see.
 */

import { store } from '../kernel';
import { getState, dispatch } from '../kernel';
import { connectionEvent } from '../actions';
import { retryDelayMs } from '../machine';
import type { ConnectionState } from '../reducers/connection';
import { closeStream, isStreamOpen, openStream } from './sse';
import { startPolling, stopPolling } from './poller';
import { startWatchdog, stopWatchdog } from './watchdog';

let unsubscribe: (() => void) | null = null;
let openTimer: ReturnType<typeof setTimeout> | null = null;
let lastApplied: string | null = null;

/** Start watching the connection phase and driving the transport from it. */
export function startTransport(): void {
	if (unsubscribe) return;
	window.addEventListener('online', onOnline);
	window.addEventListener('offline', onOffline);
	window.addEventListener('visibilitychange', onVisibility);
	unsubscribe = store.subscribe(() => apply(getState().connection));
	apply(getState().connection);
}

export function stopTransport(): void {
	unsubscribe?.();
	unsubscribe = null;
	window.removeEventListener('online', onOnline);
	window.removeEventListener('offline', onOffline);
	window.removeEventListener('visibilitychange', onVisibility);
	teardown();
	lastApplied = null;
}

function onOnline(): void {
	dispatch(connectionEvent({ type: 'navigator.online' }));
}

function onOffline(): void {
	dispatch(connectionEvent({ type: 'navigator.offline' }));
}

function onVisibility(): void {
	if (document.visibilityState === 'visible') {
		dispatch(connectionEvent({ type: 'tab.visible' }));
	}
}

// Keyed on the retry count as well as the phase: a stream that fails while
// already polling leaves the phase where it was, and a phase-only check would
// never schedule the next attempt. The counter is what says "try again".
function apply(state: ConnectionState): void {
	const key = `${state.phase}:${state.retries}`;
	if (key === lastApplied) return;
	lastApplied = key;

	switch (state.phase) {
		case 'bootstrapping':
		case 'offline':
			teardown();
			return;
		case 'connecting':
			closeStream();
			stopPolling();
			startWatchdog();
			scheduleOpen(state.retries);
			return;
		case 'streaming':
			stopPolling();
			startWatchdog();
			return;
		case 'polling':
			startPolling();
			startWatchdog();
			scheduleOpen(state.retries);
			return;
	}
}

/** Nothing is open, and nothing is waiting to open. */
function teardown(): void {
	if (openTimer !== null) {
		clearTimeout(openTimer);
		openTimer = null;
	}
	closeStream();
	stopPolling();
	stopWatchdog();
}

/**
 * Try the stream again. The first attempt is immediate: the machine reached
 * `connecting` because the session became usable, not because anything failed.
 */
function scheduleOpen(retries: number): void {
	if (openTimer !== null || isStreamOpen()) return;
	const delay = retries === 0 ? 0 : retryDelayMs(retries);
	openTimer = setTimeout(() => {
		openTimer = null;
		openStream();
	}, delay);
}
