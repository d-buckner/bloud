// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The connection machine, as a table.
 *
 * Every row is a claim about the dashboard's behaviour under a failure: what it
 * does when the proxy goes quiet, when the tab wakes up, when the stream dies
 * mid-install. Writing them out is the point: as module closures in
 * `clients/appEvents.ts` the same rules existed, but only as an interaction
 * between four `let`s and a `setInterval`, and nothing could name them.
 */

import { describe, it, expect } from 'vitest';
import {
	QUIET_BUDGET_MS,
	isQuiet,
	nextConnectionPhase,
	retryDelayMs
} from '../machine';
import type { ConnectionEvent, ConnectionPhase } from '../machine';

const T0 = 1_700_000_000_000;

const ev = (type: ConnectionEvent['type']): ConnectionEvent => ({ type }) as ConnectionEvent;

// The whole table, typed as a complete record. This is the part that keeps a
// total transition function honest: `TRANSITIONS` itself is a `Partial`, so a
// new event type would compile and silently do nothing forever. Here, a new
// phase or a new event is a type error until someone says what it means.
const EXPECTED: Record<ConnectionPhase, Record<ConnectionEvent['type'], ConnectionPhase>> = {
bootstrapping: {
'boot.done': 'connecting',
'stream.open': 'streaming',
'stream.event': 'bootstrapping',
'stream.error': 'bootstrapping',
'stream.quiet': 'bootstrapping',
'poll.ok': 'bootstrapping',
'navigator.offline': 'bootstrapping',
'navigator.online': 'bootstrapping',
'tab.visible': 'bootstrapping'
},
connecting: {
'boot.done': 'connecting',
'stream.open': 'streaming',
'stream.event': 'streaming',
'stream.error': 'connecting',
'stream.quiet': 'polling',
'poll.ok': 'connecting',
'navigator.offline': 'offline',
'navigator.online': 'connecting',
'tab.visible': 'connecting'
},
streaming: {
'boot.done': 'streaming',
'stream.open': 'streaming',
'stream.event': 'streaming',
'stream.error': 'connecting',
'stream.quiet': 'polling',
'poll.ok': 'streaming',
'navigator.offline': 'offline',
'navigator.online': 'streaming',
'tab.visible': 'connecting'
},
polling: {
'boot.done': 'polling',
'stream.open': 'streaming',
'stream.event': 'streaming',
'stream.error': 'connecting',
'stream.quiet': 'polling',
'poll.ok': 'polling',
'navigator.offline': 'offline',
'navigator.online': 'polling',
'tab.visible': 'connecting'
},
offline: {
'boot.done': 'offline',
'stream.open': 'offline',
'stream.event': 'offline',
'stream.error': 'offline',
'stream.quiet': 'offline',
'poll.ok': 'offline',
'navigator.offline': 'offline',
'navigator.online': 'connecting',
'tab.visible': 'offline'
}
};

describe('nextConnectionPhase', () => {
	it('opens the stream only once the session is usable', () => {
		expect(nextConnectionPhase('bootstrapping', ev('boot.done'))).toBe('connecting');
		expect(nextConnectionPhase('bootstrapping', ev('stream.quiet'))).toBe('bootstrapping');
	});

	it('treats a stream that beats the session read as a stream', () => {
		expect(nextConnectionPhase('bootstrapping', ev('stream.open'))).toBe('streaming');
	});

	it('falls back to polling when a live stream goes quiet', () => {
		expect(nextConnectionPhase('streaming', ev('stream.quiet'))).toBe('polling');
		expect(nextConnectionPhase('connecting', ev('stream.quiet'))).toBe('polling');
	});

	it('goes back to streaming the moment the stream proves itself, from either state', () => {
		expect(nextConnectionPhase('polling', ev('stream.open'))).toBe('streaming');
		expect(nextConnectionPhase('polling', ev('stream.event'))).toBe('streaming');
		expect(nextConnectionPhase('connecting', ev('stream.event'))).toBe('streaming');
	});

	it('treats a stream error as a reconnect, not as a failure', () => {
		expect(nextConnectionPhase('streaming', ev('stream.error'))).toBe('connecting');
		expect(nextConnectionPhase('polling', ev('stream.error'))).toBe('connecting');
	});

	it('does not leave polling because a poll succeeded: polling is already the fallback', () => {
		expect(nextConnectionPhase('polling', ev('poll.ok'))).toBe('polling');
	});

	it('goes offline on the browser signal and recovers on the same one', () => {
		expect(nextConnectionPhase('streaming', ev('navigator.offline'))).toBe('offline');
		expect(nextConnectionPhase('polling', ev('navigator.offline'))).toBe('offline');
		expect(nextConnectionPhase('offline', ev('navigator.online'))).toBe('connecting');
	});

	it('is total and explicit: every phase against every event, declared', () => {
		for (const phase of Object.keys(EXPECTED) as ConnectionPhase[]) {
			for (const type of Object.keys(EXPECTED[phase]) as ConnectionEvent['type'][]) {
				expect(nextConnectionPhase(phase, ev(type)), `${phase} + ${type}`).toBe(
					EXPECTED[phase][type]
				);
			}
		}
	});
});

describe('isQuiet', () => {
	it('stays off before any event until the budget has passed since start', () => {
		expect(isQuiet(T0, 0, T0 + QUIET_BUDGET_MS - 1, QUIET_BUDGET_MS)).toBe(false);
		expect(isQuiet(T0, 0, T0 + QUIET_BUDGET_MS, QUIET_BUDGET_MS)).toBe(true);
	});

	it('stays off while events keep arriving inside the budget', () => {
		const last = T0 + QUIET_BUDGET_MS + 1_000;
		expect(isQuiet(T0, last, last + QUIET_BUDGET_MS - 1, QUIET_BUDGET_MS)).toBe(false);
	});

	it('measures from the most recent event, not from start', () => {
		const last = T0 + 5 * QUIET_BUDGET_MS;
		expect(isQuiet(T0, last, last + 1_000, QUIET_BUDGET_MS)).toBe(false);
	});

	it('says nothing when the machine has not started: no clock, no verdict', () => {
		expect(isQuiet(0, 0, T0, QUIET_BUDGET_MS)).toBe(false);
	});
});

describe('retryDelayMs', () => {
	it('waits longer on each consecutive failure and stops growing', () => {
		expect(retryDelayMs(1)).toBe(1_000);
		expect(retryDelayMs(2)).toBe(2_000);
		expect(retryDelayMs(3)).toBe(4_000);
		expect(retryDelayMs(20)).toBe(30_000);
	});
});
