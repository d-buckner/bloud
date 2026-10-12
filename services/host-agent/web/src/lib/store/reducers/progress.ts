// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Progress slice - live per-app install progress from the event stream.
 *
 * Replaces `stores/appProgress.ts`. Separate from the apps slice on purpose:
 * grid state comes from snapshots and progress comes from node/pull events, and
 * merging the two would make every pull tick rewrite the grid.
 *
 * The merge rules are pure functions with the reducer as their only caller, so
 * a transition can be tested without a store. They came over from the old module
 * unchanged in behaviour; the tests came with them.
 */

import type { HomeData } from '$lib/types';
import { HOME_SNAPSHOT, PROGRESS_ACTIVITY, PROGRESS_NODE, PROGRESS_PULL } from '../actions';
import type { Action } from '../actions';
import { MAX_PHASE_HISTORY, PHASE_ORDER } from '../wire';
import type { ActivityEvent, NodeEvent, PullEvent } from '../wire';
import { readPayload } from './readPayload';

export type { ActivityEvent, NodeEvent, PullEvent };
export { PHASE_ORDER };

export interface ComponentProgress {
	phase: string;
	error?: string;
}

export interface PhaseHistoryEntry {
	phase: string;
	at: number;
}

export interface AppProgress {
	/** Current user-facing phase (see PHASE_ORDER); null until first event. */
	phase: string | null;
	/** Last phase before a failure: the timeline marks it as the failed step. */
	lastPhase: string | null;
	/** Pull progress detail, e.g. "34% (340.0 MiB of 1.0 GiB)". */
	phaseDetail: string;
	/** Pull percentage 0-100 when known. */
	percent: number | null;
	/** Most recent node error text. */
	error: string;
	/** Per-container phases, for multi-container apps. */
	components: Record<string, ComponentProgress>;
	/** Bounded phase transition history (oldest first) for timeline timestamps. */
	phaseHistory: PhaseHistoryEntry[];
	updatedAt: number;
}

export type ProgressMap = Record<string, AppProgress>;

export interface ProgressState {
	byId: ProgressMap;
	/** Bounded orchestrator activity ring, newest last. */
	activity: ActivityEvent[];
}

export const initialProgress: ProgressState = { byId: {}, activity: [] };

const PHASE_RANK: Record<string, number> = Object.fromEntries(PHASE_ORDER.map((p, i) => [p, i]));

export function emptyProgress(now: number): AppProgress {
	return {
		phase: null,
		lastPhase: null,
		phaseDetail: '',
		percent: null,
		error: '',
		components: {},
		phaseHistory: [],
		updatedAt: now
	};
}

function phaseRank(phase: string | null): number {
	if (phase === 'failed') return Number.MAX_SAFE_INTEGER;
	return phase ? (PHASE_RANK[phase] ?? -1) : -1;
}

function pushHistory(entry: AppProgress, phase: string, at: number): void {
	const last = entry.phaseHistory[entry.phaseHistory.length - 1];
	// Same phase again: keep the original timestamp, or the timeline drifts.
	if (last && last.phase === phase) return;
	entry.phaseHistory = [...entry.phaseHistory, { phase, at }].slice(-MAX_PHASE_HISTORY);
}

/**
 * The app-level phase given all its container components: failed dominates;
 * otherwise the least-advanced phase wins, so the app reflects remaining work.
 * A container still starting keeps the app out of "Ready" even if another
 * reached running. Single-container apps just take that container's phase.
 */
function aggregatePhase(components: Record<string, ComponentProgress>, nodePhase: string): string {
	if (Object.keys(components).length <= 1) return nodePhase;
	let phase = 'running';
	for (const comp of Object.values(components)) {
		if (comp.phase === 'failed') return 'failed';
		if (phaseRank(comp.phase) < phaseRank(phase)) phase = comp.phase;
	}
	return phase;
}

/** Merge one graph transition into the map. */
export function mergeNodeEvent(map: ProgressMap, node: NodeEvent, now = Date.now()): ProgressMap {
	const entry = map[node.app] ? { ...map[node.app] } : emptyProgress(now);

	entry.components = {
		...entry.components,
		[node.container]: { phase: node.phase, error: node.error || undefined }
	};

	const phase = aggregatePhase(entry.components, node.phase);
	if (phase !== entry.phase) {
		if (phase === 'failed' && entry.phase) {
			entry.lastPhase = entry.lastPhase ?? entry.phase;
		}
		entry.phase = phase;
		pushHistory(entry, phase, now);
	}

	if (node.error) entry.error = node.error;
	if (phase === 'running') {
		entry.error = '';
		entry.percent = null;
		entry.phaseDetail = '';
	}

	entry.updatedAt = now;
	return { ...map, [node.app]: entry };
}

/**
 * Merge one image-pull event. While pulling, the phase becomes `pulling` unless
 * a later phase was already reached; on done the detail clears and the next node
 * event advances the phase.
 */
export function mergePullEvent(map: ProgressMap, pull: PullEvent, now = Date.now()): ProgressMap {
	const entry = map[pull.app] ? { ...map[pull.app] } : emptyProgress(now);

	if (pull.phase === 'pulling') {
		if (phaseRank(entry.phase) < 1 || entry.phase === null) {
			entry.phase = 'pulling';
			pushHistory(entry, 'pulling', now);
		}
		entry.phaseDetail = pull.detail ?? '';
		entry.percent = pull.percent ?? null;
	} else {
		entry.phaseDetail = '';
	}

	entry.updatedAt = now;
	return { ...map, [pull.app]: entry };
}

/**
 * Fence the progress map against a snapshot: drop apps that no longer exist and
 * drop apps that are settled, so a completed install stops carrying a phase
 * history nobody will look at again.
 */
export function pruneToSnapshot(map: ProgressMap, data: HomeData): ProgressMap {
	const statuses = new Map(data.apps.map((a) => [a.catalog_id, a.status]));
	const next: ProgressMap = {};
	for (const [id, entry] of Object.entries(map)) {
		const status = statuses.get(id);
		if (status === undefined) continue;
		if (status === 'running' && entry.phase === 'running') continue;
		next[id] = entry;
	}
	return next;
}

export function progress(state = initialProgress, action: Action): ProgressState {
	switch (action.type) {
		case PROGRESS_NODE:
			return { ...state, byId: mergeNodeEvent(state.byId, readPayload<NodeEvent>(action)) };
		case PROGRESS_PULL:
			return { ...state, byId: mergePullEvent(state.byId, readPayload<PullEvent>(action)) };
		case PROGRESS_ACTIVITY: {
			const evt = readPayload<ActivityEvent>(action);
			return { ...state, activity: [...state.activity, evt].slice(-MAX_PHASE_HISTORY) };
		}
		case HOME_SNAPSHOT:
			return { ...state, byId: pruneToSnapshot(state.byId, readPayload<HomeData>(action)) };
		default:
			return state;
	}
}
