// SPDX-License-Identifier: AGPL-3.0-only
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
	/** Pull progress detail for the pulling step, e.g. "34% (…)". */
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
 * State of a step when the install has failed: everything before the failed
 * phase is done, the failed phase itself is 'failed', the rest pending.
 */
function failedStepState(step: FlowStep, failedPhase: string | null): TimelineStep['state'] {
	if (step.phase === null) return 'done'; // accepted always completes
	if (phaseRank(step.phase) < phaseRank(failedPhase)) return 'done';
	return step.phase === failedPhase ? 'failed' : 'pending';
}

/**
 * State of a step during a live (non-failed) install: done before the
 * current phase, 'current' at it, pending after.
 */
function liveStepState(
	step: FlowStep,
	currentPhase: string | null,
	currentRank: number
): TimelineStep['state'] {
	if (step.phase === null) return 'done'; // accepted
	if (phaseRank(step.phase) < currentRank) return 'done';
	return step.phase === currentPhase ? 'current' : 'pending';
}

/** The pulling step carries the live pull detail while it is current/done. */
function pullDetail(
	step: FlowStep,
	state: TimelineStep['state'],
	progress: AppProgress | null
): string | undefined {
	const show = step.phase === 'pulling' && (state === 'current' || state === 'done');
	return show ? progress?.phaseDetail || undefined : undefined;
}

/** The phase the install is on, defaulting queued/starting by status. */
function resolveCurrentPhase(status: string, phase: string | null): string | null {
	if (phase) return phase;
	if (status === 'installing') return 'queued';
	if (status === 'starting') return 'starting';
	return null;
}

/** The phase an install got stuck on, or null when not failed. */
function resolveFailedPhase(
	failed: boolean,
	progress: AppProgress | null,
	currentPhase: string | null
): string | null {
	if (!failed) return null;
	return progress?.lastPhase ?? currentPhase ?? 'queued';
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
		return history.find((h) => h.phase === ph)?.at;
	};

	const currentPhase = resolveCurrentPhase(status, phase);
	const currentRank = phaseRank(currentPhase);
	const failed = status === 'failed' || status === 'error';
	const failedPhase = resolveFailedPhase(failed, progress, currentPhase);

	return INSTALL_FLOW.map((step) => {
		const state =
			status === 'running'
				? 'done'
				: failed
					? failedStepState(step, failedPhase)
					: liveStepState(step, currentPhase, currentRank);
		return {
			id: step.id,
			label: step.label,
			state,
			at: timeFor(step.phase),
			detail: pullDetail(step, state, progress)
		};
	});
}
