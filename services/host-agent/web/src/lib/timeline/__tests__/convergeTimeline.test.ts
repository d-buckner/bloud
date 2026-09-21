// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { buildSteps, scanConverge, CONVERGE_STEPS, type ConvergeScan } from '../convergeTimeline';
import type { OrchestratorActivity } from '$lib/clients/developerClient';

const act = (event: string, detail = '', time = 't'): OrchestratorActivity => ({ event, detail, time });

const emptyScan = (): ConvergeScan => ({
	completedSteps: new Map(),
	cycleComplete: false,
	convergeDuration: null,
	convergeTime: null,
	hasCycle: false
});

const stepNames = (scan: ConvergeScan) => [...scan.completedSteps.keys()];
const statusOf = (steps: { name: string; status: string }[]) =>
	Object.fromEntries(steps.map((s) => [s.name, s.status]));

describe('scanConverge', () => {
	it('empty activity yields no cycle and no steps', () => {
		expect(scanConverge([])).toEqual(emptyScan());
	});

	it('records converge_step names (text before " (") as completed', () => {
		const scan = scanConverge([
			act('converge_step', 'update-graph (2 nodes)'),
			act('converge_step', 'reconcile (ok)')
		]);
		expect(scan.hasCycle).toBe(true);
		expect(stepNames(scan)).toEqual(['update-graph', 'reconcile']);
		expect(scan.completedSteps.get('update-graph')).toBe('update-graph (2 nodes)');
	});

	it('converge_complete marks the cycle done and extracts the duration tail', () => {
		const scan = scanConverge([act('converge_complete', '6 steps, updated 2, 1.4s', 'T9')]);
		expect(scan.cycleComplete).toBe(true);
		expect(scan.convergeDuration).toBe('1.4s');
		expect(scan.convergeTime).toBe('T9');
	});

	it('a single-part converge_complete has no duration', () => {
		expect(scanConverge([act('converge_complete', 'done')]).convergeDuration).toBeNull();
	});

	it('converge_start sets hasCycle and stops the scan (later steps ignored)', () => {
		const scan = scanConverge([
			act('converge_complete', 'a, 9s', 'T1'),
			act('converge_start', 'begin', 'T2'),
			act('converge_step', 'ignored (x)', 'T3')
		]);
		expect(scan.cycleComplete).toBe(true);
		expect(scan.convergeTime).toBe('T1');
		expect(scan.hasCycle).toBe(true);
		expect(scan.completedSteps.size).toBe(0);
	});
});

describe('buildSteps', () => {
	it('all pending when nothing completed and not converging', () => {
		const steps = buildSteps(emptyScan(), false);
		expect(steps.map((s) => s.name)).toEqual([...CONVERGE_STEPS]);
		expect(steps.every((s) => s.status === 'pending')).toBe(true);
	});

	it('completed steps are done and carry their detail', () => {
		const scan: ConvergeScan = {
			...emptyScan(),
			completedSteps: new Map([['update-graph', 'update-graph (2)']])
		};
		expect(statusOf(buildSteps(scan, false))['update-graph']).toBe('done');
		expect(buildSteps(scan, false).find((s) => s.name === 'update-graph')?.detail).toBe(
			'update-graph (2)'
		);
	});

	it('converging with no completed cycle marks the first pending step active', () => {
		const steps = buildSteps(emptyScan(), true);
		expect(steps[0].status).toBe('active');
		expect(steps.slice(1).every((s) => s.status === 'pending')).toBe(true);
	});

	it('a completed cycle turns every pending step done', () => {
		const scan: ConvergeScan = { ...emptyScan(), cycleComplete: true };
		expect(buildSteps(scan, true).every((s) => s.status === 'done')).toBe(true);
	});
});
