// SPDX-License-Identifier: AGPL-3.0-only
import { describe, it, expect } from 'vitest';
import { frameStatus } from '../reconcilerView';
import type { OrchestratorStatus } from '$lib/clients/developerClient';

const NOW = Date.parse('2026-10-10T12:00:00Z');

function status(over: Partial<OrchestratorStatus> = {}): OrchestratorStatus {
	return {
		queueDepth: 0,
		isConverging: false,
		recentActivity: [],
		lastConverged: new Date(NOW - 20_000).toISOString(),
		...over
	};
}

describe('frameStatus', () => {
	it('says nothing when the engine is idle and recent', () => {
		expect(frameStatus(status(), NOW)).toEqual({ note: '', tone: 'idle' });
	});

	it('says the loop is stopped before it says anything else', () => {
		// A stopped loop still reports a recent pass and an empty queue, which is
		// otherwise indistinguishable from a healthy idle engine.
		const s = status({ loopStopped: true });
		expect(frameStatus(s, NOW)).toEqual({ note: 'reconciler stopped', tone: 'error' });
	});

	it('names a pass in flight and how much is waiting behind it', () => {
		expect(frameStatus(status({ isConverging: true }), NOW)).toEqual({
			note: 'converging',
			tone: 'info'
		});
		expect(frameStatus(status({ isConverging: true, queueDepth: 2 }), NOW)).toEqual({
			note: 'converging · 2 queued',
			tone: 'info'
		});
	});

	it('reports a queue even between passes', () => {
		expect(frameStatus(status({ queueDepth: 3 }), NOW)).toEqual({
			note: '3 queued',
			tone: 'info'
		});
	});

	it('says nothing about an age that is not yet news', () => {
		// The self-heal timer runs on an idle ~60s, so four minutes is one missed
		// pass at most and printing it would be a clock, not a warning.
		expect(frameStatus(status({ lastConverged: new Date(NOW - 4 * 60_000).toISOString() }), NOW).note).toBe('');
	});

	it('prints the age once several passes are overdue', () => {
		expect(frameStatus(status({ lastConverged: new Date(NOW - 12 * 60_000).toISOString() }), NOW)).toEqual(
			{ note: 'last pass 12m ago', tone: 'warn' }
		);
	});

	it('says when no pass has ever finished', () => {
		expect(frameStatus(status({ lastConverged: '' }), NOW)).toEqual({
			note: 'no pass yet',
			tone: 'warn'
		});
	});

	it('says there is no engine at all', () => {
		expect(frameStatus(undefined, NOW)).toEqual({
			note: 'reconciler unavailable',
			tone: 'error'
		});
	});
});
