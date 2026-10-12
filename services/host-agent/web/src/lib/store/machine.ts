// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The connection machine - one of the three state machines in the store.
 *
 * It answers one question, and nothing else does: is the dashboard getting live
 * state from the stream, from the fallback poll, or from neither? Every piece of
 * that knowledge used to be a module closure in `clients/appEvents.ts`
 * (`started`, `fallbackActive`, `lastEventAt`, `watchdog`), which meant a
 * component could not ask it and a test could not reach it.
 *
 * `nextConnectionPhase` is pure and total: an event with no rule for the current
 * phase leaves the phase alone, which is what makes the transition table below
 * the whole story. The effects module reads the phase and opens or closes the
 * transport; it never decides anything itself.
 */

export type ConnectionPhase = 'bootstrapping' | 'connecting' | 'streaming' | 'polling' | 'offline';

export type ConnectionEvent =
	/** Session is usable: open the stream. */
	| { type: 'boot.done' }
	/** EventSource fired `onopen`. */
	| { type: 'stream.open' }
	/** Any message arrived: proof the transport is alive. */
	| { type: 'stream.event' }
	/** EventSource fired `onerror`, or the fetch behind a poll failed. */
	| { type: 'stream.error' }
	/** The watchdog saw no event of any kind for FALLBACK_DELAY_MS. */
	| { type: 'stream.quiet' }
	/** A fallback poll succeeded. */
	| { type: 'poll.ok' }
	/** A fallback poll failed. */
	| { type: 'poll.error' }
	/** `navigator.onLine` went false. */
	| { type: 'navigator.offline' }
	/** `navigator.onLine` came back. */
	| { type: 'navigator.online' }
	/**
	 * The tab was hidden and is now visible again. A backgrounded tab gets its
	 * timers throttled and its connections left alone by the browser, so on
	 * return the stream is a coin flip. Revalidating it costs one reconnect and
	 * saves a tab sitting on a dead stream until the watchdog notices.
	 */
	| { type: 'tab.visible' };

/**
 * The transition table, as data. A phase with no entry for an event keeps its
 * phase, so the table only has to list what changes.
 */
const TRANSITIONS: Partial<Record<ConnectionPhase, Partial<Record<ConnectionEvent['type'], ConnectionPhase>>>> = {
	bootstrapping: {
		'boot.done': 'connecting',
		// A stream that beats the session read is still a stream: trust it.
		'stream.open': 'streaming'
	},
	connecting: {
		'stream.open': 'streaming',
		// Already reopening: nothing to revalidate.
		'tab.visible': 'connecting',
		'stream.event': 'streaming',
		'stream.quiet': 'polling',
		'navigator.offline': 'offline'
	},
	streaming: {
		'stream.error': 'connecting',
		'tab.visible': 'connecting',
		'stream.quiet': 'polling',
		'navigator.offline': 'offline'
	},
	polling: {
		// Any stream proof wins: the poller is the fallback, not the plan.
		'tab.visible': 'connecting',
		'stream.open': 'streaming',
		'stream.event': 'streaming',
		'stream.error': 'connecting',
		'navigator.offline': 'offline'
	},
	offline: {
		'navigator.online': 'connecting'
	}
};

/** Pure lookup. Unknown pair, or unknown phase: the phase is unchanged. */
export function nextConnectionPhase(current: ConnectionPhase, event: ConnectionEvent): ConnectionPhase {
	return TRANSITIONS[current]?.[event.type] ?? current;
}

/**
 * Whether the transport is silent long enough to be treated as broken.
 * `lastEventAt` of 0 means "nothing has arrived yet", so the clock runs from
 * when the machine started instead. This is `shouldEnableFallback` from
 * `clients/appEvents.ts`, with the same semantics and now a real test.
 */
export function isQuiet(startedAt: number, lastEventAt: number, now: number, budgetMs: number): boolean {
	const baseline = lastEventAt > 0 ? lastEventAt : startedAt;
	if (baseline <= 0) return false;
	return now - baseline >= budgetMs;
}

/** No event of any kind for this long and the machine falls back to polling. */
export const QUIET_BUDGET_MS = 10_000;

/** How often the watchdog looks. */
export const WATCHDOG_INTERVAL_MS = 5_000;

/** Backoff for reopening a stream that keeps failing, capped so it settles. */
export function retryDelayMs(attempts: number): number {
	return Math.min(1_000 * 2 ** Math.max(0, attempts - 1), 30_000);
}
