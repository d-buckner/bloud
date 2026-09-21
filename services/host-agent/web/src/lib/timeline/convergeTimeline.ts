// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Converge timeline derivation: pure mapping from the orchestrator activity
 * log to the ordered converge-step view. Extracted from the developer page so
 * the scan/assign logic is testable without SvelteKit. The activity log is
 * newest-first (backend order preserved); scanning it for converge markers is
 * time-ordered by construction.
 */

import type { OrchestratorActivity, OrchestratorStatus } from '$lib/clients/developerClient';

/** The converge pipeline steps, in order. */
export const CONVERGE_STEPS = [
	'sync-container-state',
	'handle-uninstalls',
	'set-graph-targets',
	'converge-tailnet',
	'update-graph',
	'reconcile'
];

export interface TimelineStep {
	name: string;
	status: 'done' | 'active' | 'pending';
	detail: string;
}

export interface Timeline {
	recentIntents: { detail: string; time: string }[];
	drain: { detail: string; time: string } | null;
	steps: TimelineStep[];
	convergeDuration: string | null;
	convergeTime: string | null;
	hasCycle: boolean;
}

/** Result of scanning the activity log for converge lifecycle markers. */
export interface ConvergeScan {
	completedSteps: Map<string, string>;
	cycleComplete: boolean;
	convergeDuration: string | null;
	convergeTime: string | null;
	hasCycle: boolean;
}

/**
 * Scan the activity log for converge markers.
 *
 *  - `converge_step`  → record the step (name before " (") as completed
 *  - `converge_complete` → cycle finished; duration is the last ", "-part
 *  - `converge_start` → a new cycle began; stops the scan (the step detail
 *    of the previous cycle is no longer relevant)
 */
export function scanConverge(activity: OrchestratorActivity[]): ConvergeScan {
	const completedSteps = new Map<string, string>();
	let cycleComplete = false;
	let convergeDuration: string | null = null;
	let convergeTime: string | null = null;
	let hasCycle = false;

	for (const entry of activity) {
		if (entry.event === 'converge_complete') {
			cycleComplete = true;
			const parts = entry.detail.split(', ');
			convergeDuration = parts.length > 1 ? parts[parts.length - 1] : null;
			convergeTime = entry.time;
			continue;
		}
		if (entry.event === 'converge_start') {
			hasCycle = true;
			break;
		}
		if (entry.event === 'converge_step') {
			hasCycle = true;
			const stepName = entry.detail.split(' (')[0];
			completedSteps.set(stepName, entry.detail);
		}
	}

	return { completedSteps, cycleComplete, convergeDuration, convergeTime, hasCycle };
}

/**
 * Build the ordered step list from a scan. Completed steps are `done`; when
 * a cycle completed all remaining steps are `done`, and while a cycle is
 * actively converging the first not-yet-done step is `active`.
 */
export function buildSteps(scan: ConvergeScan, isConverging: boolean): TimelineStep[] {
	const steps: TimelineStep[] = CONVERGE_STEPS.map((name) =>
		scan.completedSteps.has(name)
			? { name, status: 'done' as const, detail: scan.completedSteps.get(name)! }
			: { name, status: 'pending' as const, detail: '' }
	);

	if (scan.cycleComplete) {
		for (const step of steps) {
			if (step.status === 'pending') step.status = 'done';
		}
	} else if (isConverging) {
		const firstPending = steps.find((s) => s.status === 'pending');
		if (firstPending) firstPending.status = 'active';
	}

	return steps;
}

/** Full timeline from an orchestrator status snapshot. */
export function parseTimeline(status: OrchestratorStatus): Timeline {
	const activity = status.recentActivity;

	const recentIntents = activity
		.filter((a) => a.event === 'intent_enqueued')
		.slice(0, 5)
		.map((a) => ({ detail: a.detail, time: a.time }));

	const lastDrain = activity.find((a) => a.event === 'drain_complete');
	const drain = lastDrain ? { detail: lastDrain.detail, time: lastDrain.time } : null;

	const scan = scanConverge(activity);

	return {
		recentIntents,
		drain,
		steps: buildSteps(scan, status.isConverging),
		convergeDuration: scan.convergeDuration,
		convergeTime: scan.convergeTime,
		hasCycle: scan.hasCycle
	};
}
