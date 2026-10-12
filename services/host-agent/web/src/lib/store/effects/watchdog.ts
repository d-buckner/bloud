// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The watchdog effect - the only thing that turns silence into a fact.
 *
 * It does not decide anything either. It asks the machine's pure `isQuiet` and,
 * when the answer is yes, dispatches `stream.quiet`. The transition to polling is
 * the table's business.
 *
 * The window it covers is the one a browser cannot report: a tab that slept and
 * woke with a socket that still looks open, or a proxy holding a dead
 * connection. EventSource's own reconnect does not fire for those.
 */

import { getState, dispatch } from '../kernel';
import { connectionEvent } from '../actions';
import { QUIET_BUDGET_MS, WATCHDOG_INTERVAL_MS, isQuiet } from '../machine';

let timer: ReturnType<typeof setInterval> | null = null;

export function startWatchdog(): void {
	if (timer !== null) return;
	timer = setInterval(tick, WATCHDOG_INTERVAL_MS);
}

export function stopWatchdog(): void {
	if (timer === null) return;
	clearInterval(timer);
	timer = null;
}

function tick(): void {
	const { startedAt, lastEventAt } = getState().connection;
	if (isQuiet(startedAt, lastEventAt, Date.now(), QUIET_BUDGET_MS)) {
		dispatch(connectionEvent({ type: 'stream.quiet' }));
	}
}
