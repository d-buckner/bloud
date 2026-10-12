// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Connection slice - the connection machine's state.
 *
 * The phase itself is decided by `nextConnectionPhase` in `../machine.ts`; this
 * reducer only applies the decision and keeps the two clocks the watchdog needs.
 * Those clocks used to be module-level `let`s in `clients/appEvents.ts`, which
 * is why no component could render "reconnecting" and no test could set one.
 */

import { CONNECTION_EVENT, CONNECTION_RESET } from '../actions';
import type { Action } from '../actions';
import { nextConnectionPhase } from '../machine';
import type { ConnectionEvent, ConnectionPhase } from '../machine';
import { readPayload } from './readPayload';

export interface ConnectionState {
	phase: ConnectionPhase;
	/** When the machine started watching, used until the first event lands. */
	startedAt: number;
	/** When the last event of any kind arrived. 0 means never. */
	lastEventAt: number;
	/** Consecutive stream failures since the last successful open. */
	retries: number;
}

export const initialConnection: ConnectionState = {
	phase: 'bootstrapping',
	startedAt: 0,
	lastEventAt: 0,
	retries: 0
};

export function connection(state = initialConnection, action: Action): ConnectionState {
	if (action.type === CONNECTION_RESET) {
		return { ...initialConnection, startedAt: 0 };
	}
	if (action.type !== CONNECTION_EVENT) return state;

	const event = readPayload<ConnectionEvent>(action);
	const phase = nextConnectionPhase(state.phase, event);
	const now = nowOf(event);

	return {
		phase,
		startedAt: state.startedAt || now,
		lastEventAt: isLiveness(event) ? now : state.lastEventAt,
		retries: nextRetries(state.retries, event)
	};
}

/** Events that prove the transport is carrying something. */
function isLiveness(event: ConnectionEvent): boolean {
	return event.type === 'stream.event' || event.type === 'poll.ok' || event.type === 'stream.open';
}

function nowOf(event: ConnectionEvent): number {
	return event.type === 'stream.open' || isLiveness(event) ? Date.now() : 0;
}

function nextRetries(retries: number, event: ConnectionEvent): number {
	if (event.type === 'stream.open' || event.type === 'poll.ok') return 0;
	if (event.type === 'stream.error') return retries + 1;
	return retries;
}
