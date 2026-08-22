// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { describe, it, expect } from 'vitest';
import { deriveTimeline } from '../installTimeline';
import type { AppProgress } from '$lib/stores/appProgress';

const NOW = 1_700_000_000_000;

function progress(phase: string | null, extra: Partial<AppProgress> = {}): AppProgress {
	return {
		phase,
		lastPhase: null,
		phaseDetail: '',
		percent: null,
		error: '',
		components: {},
		phaseHistory: phase ? [{ phase, at: NOW }] : [],
		updatedAt: NOW,
		...extra
	};
}

const states = (steps: { id: string; state: string; label?: string }[]) =>
	steps.map(({ id, state }) => ({ id, state }));

describe('deriveTimeline', () => {
	it('fresh install with no live progress: accepted done, planned current', () => {
		const steps = deriveTimeline('installing', null);
		expect(states(steps)).toEqual([
			{ id: 'accepted', state: 'done' },
			{ id: 'planned', state: 'current' },
			{ id: 'pulling', state: 'pending' },
			{ id: 'configuring', state: 'pending' },
			{ id: 'starting', state: 'pending' },
			{ id: 'finalizing', state: 'pending' },
			{ id: 'ready', state: 'pending' }
		]);
	});

	it('pulling at 34%: pulling current with detail', () => {
		const steps = deriveTimeline('installing', progress('pulling', { percent: 34, phaseDetail: '34% — 340.0 MiB of 1.0 GiB' }));
		const pulling = steps.find((s) => s.id === 'pulling');
		expect(pulling?.state).toBe('current');
		expect(pulling?.detail).toBe('34% — 340.0 MiB of 1.0 GiB');
		expect(steps.find((s) => s.id === 'planned')?.state).toBe('done');
		expect(steps.find((s) => s.id === 'starting')?.state).toBe('pending');
	});

	it('running: every step done', () => {
		const steps = deriveTimeline('running', progress('running'));
		expect(steps.every((s) => s.state === 'done')).toBe(true);
	});

	it('failed at starting: earlier done, starting failed, later pending', () => {
		const steps = deriveTimeline(
			'failed',
			progress('failed', { lastPhase: 'starting', error: 'health check failed' })
		);
		expect(states(steps)).toEqual([
			{ id: 'accepted', state: 'done' },
			{ id: 'planned', state: 'done' },
			{ id: 'pulling', state: 'done' },
			{ id: 'configuring', state: 'done' },
			{ id: 'starting', state: 'failed' },
			{ id: 'finalizing', state: 'pending' },
			{ id: 'ready', state: 'pending' }
		]);
	});

	it('degraded (error) with no progress info: marks the planned step failed', () => {
		const steps = deriveTimeline('error', null);
		const failed = steps.filter((s) => s.state === 'failed');
		expect(failed).toHaveLength(1);
		expect(failed[0].id).toBe('planned');
	});

	it('carries phase timestamps from the history', () => {
		const steps = deriveTimeline('installing', progress('configuring', {
			phaseHistory: [
				{ phase: 'pulling', at: NOW },
				{ phase: 'configuring', at: NOW + 5000 }
			]
		}));
		expect(steps.find((s) => s.id === 'pulling')?.at).toBe(NOW);
		expect(steps.find((s) => s.id === 'configuring')?.at).toBe(NOW + 5000);
	});
});
