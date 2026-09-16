// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * App event client - SSE as the primary app-state source, adaptive polling
 * as a safety net.
 *
 * Events from GET /api/apps/events:
 *   snapshot  full home payload → apps + grid stores (source of truth)
 *   node      per-container lifecycle transition → appProgress store
 *   pull      image pull progress (throttled server-side) → appProgress
 *   activity  orchestrator activity ring → recentActivity
 *
 * Fallback: if no event arrives for FALLBACK_DELAY_MS (proxy weirdness, tab
 * sleep/wake, a stream killed mid-rollout), the adaptive poller starts on its
 * own. Any live event (SSE or a successful poll) suspends it again. The
 * snapshot-on-connect contract on the server side means reconnects simply
 * re-apply full state — no Last-Event-ID replay needed.
 */

import { get } from 'svelte/store';
import { apps, loading, error } from '$lib/stores/apps';
import { gridElements } from '$lib/stores/grid';
import {
	appProgress,
	mergeNodeEvent,
	mergePullEvent,
	mergeSnapshot,
	recordActivity,
	type NodeEvent,
	type PullEvent,
	type ActivityEvent
} from '$lib/stores/appProgress';
import { detectToasts, pushToast } from '$lib/stores/toasts';
import { startPolling, stopPolling } from './poller';
import type { App, HomeData } from '$lib/types';

export const FALLBACK_DELAY_MS = 10_000;
const WATCHDOG_INTERVAL_MS = 5_000;

let eventSource: EventSource | null = null;
let watchdog: ReturnType<typeof setInterval> | null = null;
let startedAt = 0;
let lastEventAt = 0;
let fallbackActive = false;
let started = false;

/** Last observed status per catalog_id, used to derive transition toasts. */
let lastStatuses = new Map<string, string>();

/**
 * Pure fallback decision: the safety-net poller should be running when no
 * live event has arrived within FALLBACK_DELAY_MS of the most recent event
 * (or of client start, before the first event).
 */
export function shouldEnableFallback(startedAt: number, lastEventAt: number, now: number): boolean {
	const baseline = lastEventAt > 0 ? lastEventAt : startedAt;
	return now - baseline >= FALLBACK_DELAY_MS;
}

/** Apply a home payload (SSE snapshot or fallback poll) to all stores. */
function handleHomeData(data: HomeData): void {
	const transitions = detectToasts(lastStatuses, data.apps);
	lastStatuses = new Map(data.apps.map((a: App) => [a.catalog_id, a.status]));

	apps.set(data.apps);
	gridElements.setFromHome(data);
	loading.set(false);
	error.set(null);
	appProgress.set(mergeSnapshot(get(appProgress), data));

	for (const t of transitions) pushToast(t.message, t.tone);
}

function touchEvent(): void {
	lastEventAt = Date.now();
	if (fallbackActive) {
		stopPolling();
		fallbackActive = false;
	}
}

function startFallback(): void {
	if (fallbackActive) return;
	fallbackActive = true;
	startPolling(handleHomeData);
}

function runWatchdog(): void {
	if (shouldEnableFallback(startedAt, lastEventAt, Date.now())) {
		startFallback();
	}
}

export function startAppEvents(): void {
	if (started || typeof EventSource === 'undefined') return;
	started = true;
	startedAt = Date.now();

	try {
		eventSource = new EventSource('/api/apps/events');
	} catch {
		eventSource = null;
	}

	eventSource?.addEventListener('snapshot', (e: MessageEvent<string>) => {
		touchEvent();
		try {
			handleHomeData(JSON.parse(e.data) as HomeData);
		} catch (err) {
			console.error('events: bad snapshot payload', err);
		}
	});

	eventSource?.addEventListener('node', (e: MessageEvent<string>) => {
		touchEvent();
		try {
			appProgress.update((m) => mergeNodeEvent(m, JSON.parse(e.data) as NodeEvent));
		} catch (err) {
			console.error('events: bad node payload', err);
		}
	});

	eventSource?.addEventListener('pull', (e: MessageEvent<string>) => {
		touchEvent();
		try {
			appProgress.update((m) => mergePullEvent(m, JSON.parse(e.data) as PullEvent));
		} catch (err) {
			console.error('events: bad pull payload', err);
		}
	});

	eventSource?.addEventListener('activity', (e: MessageEvent<string>) => {
		touchEvent();
		try {
			recordActivity(JSON.parse(e.data) as ActivityEvent);
		} catch (err) {
			console.error('events: bad activity payload', err);
		}
	});

	// The watchdog enables the polling safety net when the stream goes quiet.
	// (EventSource reconnects on its own; this covers the quiet window.)
	watchdog = setInterval(runWatchdog, WATCHDOG_INTERVAL_MS);
}

export function stopAppEvents(): void {
	started = false;
	if (eventSource) {
		eventSource.close();
		eventSource = null;
	}
	if (watchdog) {
		clearInterval(watchdog);
		watchdog = null;
	}
	stopPolling();
	fallbackActive = false;
	lastEventAt = 0;
	lastStatuses = new Map();
}
