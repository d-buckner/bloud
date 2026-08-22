// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Install timeline derivation - pure mapping from (app status, live progress)
 * to the ordered step list shown in the install modal.
 *
 * Steps: Accepted → Planned → Pulling image → Configuring → Starting →
 * Finalizing → Ready. The current step comes from the live progress store
 * (SSE node/pull events); without any progress info the app status is used
 * as a coarse fallback.
 */

import type { AppProgress } from '$lib/stores/appProgress';
import { PHASE_ORDER } from '$lib/stores/appProgress';

export interface TimelineStep {
	id: string;
	label: string;
	state: 'done' | 'current' | 'pending' | 'failed';
	/** Epoch ms when this step's phase was first observed, if known. */
	at?: number;
	/** Pull progress detail for the pulling step, e.g. "34% — …". */
	detail?: string;
}

interface FlowStep {
	id: string;
	label: string;
	phase: string | null;
}

/** The install flow in display order. `phase` null = no live phase maps to it. */
export const INSTALL_FLOW: FlowStep[] = [
	{ id: 'accepted', label: 'Accepted', phase: null },
	{ id: 'planned', label: 'Planned', phase: 'queued' },
	{ id: 'pulling', label: 'Pulling image', phase: 'pulling' },
	{ id: 'configuring', label: 'Configuring', phase: 'configuring' },
	{ id: 'starting', label: 'Starting', phase: 'starting' },
	{ id: 'finalizing', label: 'Finalizing', phase: 'finalizing' },
	{ id: 'ready', label: 'Ready', phase: 'running' }
];

function phaseRank(phase: string | null): number {
	if (!phase) return -1;
	const idx = (PHASE_ORDER as readonly string[]).indexOf(phase);
	return idx === -1 ? -1 : idx;
}

/**
 * Derive the timeline steps.
 *
 *  - running  → every step done
 *  - failed / error → steps before the failed phase are done, the failed
 *    phase (lastPhase when known, else the current phase) is marked failed
 *  - installing / starting → steps up to the current live phase are done /
 *    current, the rest pending
 */
export function deriveTimeline(status: string, progress: AppProgress | null): TimelineStep[] {
	const phase = progress?.phase ?? null;
	const history = progress?.phaseHistory ?? [];

	const timeFor = (ph: string | null): number | undefined => {
		if (!ph) return undefined;
		const entry = history.find((h) => h.phase === ph);
		return entry?.at;
	};

	// The phase the install is (or was) stuck on.
	let currentPhase: string | null = phase;
	if (!currentPhase) {
		if (status === 'installing') currentPhase = 'queued';
		else if (status === 'starting') currentPhase = 'starting';
	}
	const currentRank = phaseRank(currentPhase);
	const failed = status === 'failed' || status === 'error';
	const failedPhase = failed ? (progress?.lastPhase ?? currentPhase ?? 'queued') : null;
	const failedRank = phaseRank(failedPhase);

	const steps: TimelineStep[] = INSTALL_FLOW.map((step) => {
		const rank = phaseRank(step.phase);
		let state: TimelineStep['state'];

		if (status === 'running') {
			state = 'done';
		} else if (failed) {
			if (step.phase === null) state = 'done'; // accepted always completes
			else if (rank < failedRank) state = 'done';
			else if (step.phase === failedPhase) state = 'failed';
			else state = 'pending';
		} else if (step.phase === null) {
			state = 'done'; // accepted
		} else if (rank < currentRank) {
			state = 'done';
		} else if (step.phase === currentPhase) {
			state = 'current';
		} else {
			state = 'pending';
		}

		const at = timeFor(step.phase);
		return {
			id: step.id,
			label: step.label,
			state,
			at,
			detail: step.phase === 'pulling' && (state === 'current' || state === 'done')
				? progress?.phaseDetail || undefined
				: undefined
		};
	});

	return steps;
}
