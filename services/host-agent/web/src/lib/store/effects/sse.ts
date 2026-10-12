// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The SSE effect - the socket, and nothing else.
 *
 * It opens, it closes, it turns bytes into actions. It decides nothing: whether
 * the socket should be open is the connection machine's answer, and this module
 * reads it. That split is what `clients/appEvents.ts` never had, which is why its
 * `started`, `fallbackActive`, and `lastEventAt` had to be module state.
 *
 * The server's contract is snapshot-on-connect: every new stream, and every
 * reconnect, starts with a full `snapshot`. So a gap in the stream is healed by
 * the reconnect itself and no replay cursor is needed.
 */

import { dispatch } from '../kernel';
import type { Action } from '../actions';
import { connectionEvent, homeSnapshot, progressActivity, progressNode, progressPull } from '../actions';
import type { ActivityEvent, NodeEvent, PullEvent } from '../wire';
import type { HomeData } from '$lib/types';

const STREAM_URL = '/api/apps/events';

let source: EventSource | null = null;

export function isStreamOpen(): boolean {
	return source !== null;
}

/** Open the stream. Calling it while one is open is a no-op, not a second socket. */
export function openStream(): void {
	if (source !== null || typeof EventSource === 'undefined') return;
	try {
		source = new EventSource(STREAM_URL);
	} catch {
		source = null;
		return;
	}

	source.addEventListener('open', () => dispatch(connectionEvent({ type: 'stream.open' })));
	source.addEventListener('error', () => dispatch(connectionEvent({ type: 'stream.error' })));

	bind<HomeData>('snapshot', homeSnapshot);
	bind<NodeEvent>('node', progressNode);
	bind<PullEvent>('pull', progressPull);
	bind<ActivityEvent>('activity', progressActivity);
}

export function closeStream(): void {
	if (!source) return;
	source.close();
	source = null;
}

/**
 * One named event: report liveness first, then apply the payload. The order is
 * the whole point. A payload that lands before the liveness signal would let the
 * watchdog see silence and fall back to polling on a stream that is working.
 */
function bind<P>(name: string, to: (payload: P) => Action): void {
	source?.addEventListener(name, (event: MessageEvent<string>) => {
		dispatch(connectionEvent({ type: 'stream.event' }));
		try {
			dispatch(to(JSON.parse(event.data) as P));
		} catch (err) {
			console.error(`events: bad ${name} payload`, err);
		}
	});
}
