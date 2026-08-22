// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * App progress store - live per-app install progress from the SSE event stream.
 *
 * Holds user-facing phase info (phase, pull percent/detail, per-component
 * phases, errors) keyed by catalog_id. It is separate from the `apps` store
 * on purpose: grid/layout state comes from snapshots, progress comes from
 * node/pull events, and merging them in one store would make every pull
 * tick rewrite grid state.
 *
 * The merge functions are pure so they can be unit tested.
 */

import { writable } from 'svelte/store';
import type { HomeData } from '$lib/types';

export interface NodeEvent {
	app: string;
	container: string;
	phase: string;
	error?: string;
}

export interface PullEvent {
	app: string;
	image: string;
	phase: 'pulling' | 'done';
	percent?: number;
	detail?: string;
}

export interface ActivityEvent {
	time: string;
	event: string;
	detail: string;
}

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
	/** Last phase before a failure — the timeline marks it as the failed step. */
	lastPhase: string | null;
	/** Pull progress detail, e.g. "34% — 340.0 MiB of 1.0 GiB". */
	phaseDetail: string;
	/** Pull percentage 0-100 when known. */
	percent: number | null;
	/** Most recent node error text. */
	error: string;
	/** Per-container phases (multi-container apps). */
	components: Record<string, ComponentProgress>;
	/** Bounded phase transition history (oldest first) for timeline timestamps. */
	phaseHistory: PhaseHistoryEntry[];
	updatedAt: number;
}

export type ProgressMap = Record<string, AppProgress>;

/** User-facing phase order, from earliest to done. */
export const PHASE_ORDER = ['queued', 'pulling', 'configuring', 'starting', 'finalizing', 'running'] as const;

const PHASE_RANK: Record<string, number> = Object.fromEntries(PHASE_ORDER.map((p, i) => [p, i]));
const MAX_HISTORY = 12;

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
	if (last && last.phase === phase) return; // same phase: keep original timestamp
	entry.phaseHistory = [...entry.phaseHistory, { phase, at }].slice(-MAX_HISTORY);
}

/**
 * Merge one node (graph transition) event into the progress map.
 * The app-level phase is the most advanced phase across its containers
 * (failed dominates).
 */
export function mergeNodeEvent(map: ProgressMap, node: NodeEvent, now = Date.now()): ProgressMap {
	const entry = map[node.app] ? { ...map[node.app] } : emptyProgress(now);

	entry.components = {
		...entry.components,
		[node.container]: { phase: node.phase, error: node.error || undefined }
	};

	// Aggregate across components: failed dominates; otherwise the least
	// advanced phase wins — the app's phase reflects the remaining work, so a
	// container still starting keeps the app out of "Ready" even if another
	// container already reached running.
	let phase: string = node.phase;
	if (Object.keys(entry.components).length > 1) {
		phase = 'running';
		for (const comp of Object.values(entry.components)) {
			if (comp.phase === 'failed') {
				phase = 'failed';
				break;
			}
			if (phaseRank(comp.phase) < phaseRank(phase)) phase = comp.phase;
		}
	}

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
 * Merge one pull progress event. While pulling, the app phase becomes
 * 'pulling' (unless a later phase was already reached); on done the detail
 * is cleared and the next node event advances the phase.
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
		// done: the pull finished; keep the final percent briefly but drop
		// the detail so the tile stops showing a progress bar.
		entry.phaseDetail = '';
	}

	entry.updatedAt = now;
	return { ...map, [pull.app]: entry };
}

/**
 * Reconcile the progress map with a snapshot: drop entries for apps that no
 * longer exist and reset progress for apps that are stably running.
 */
export function mergeSnapshot(map: ProgressMap, data: HomeData): ProgressMap {
	const statuses = new Map(data.apps.map((a) => [a.catalog_id, a.status]));
	const next: ProgressMap = {};
	for (const [id, entry] of Object.entries(map)) {
		const status = statuses.get(id);
		if (status === undefined) continue; // app gone (uninstalled) → dropped
		if (status === 'running' && entry.phase === 'running') continue; // settled
		next[id] = entry;
	}
	return next;
}

/** The live progress store, keyed by catalog_id. */
export const appProgress = writable<ProgressMap>({});

/** Bounded recent orchestrator activity (newest last), for the install modal. */
export const recentActivity = writable<ActivityEvent[]>([]);

/** Record one activity event on the ring (bounded to MAX_HISTORY entries). */
export function recordActivity(evt: ActivityEvent): void {
	recentActivity.update((current) => [...current, evt].slice(-MAX_HISTORY));
}
