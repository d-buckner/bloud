/**
 * Home Poller - Replaces SSE with adaptive polling of GET /api/user/home
 *
 * Uses a short interval while any app is transitioning (installing, starting,
 * uninstalling), and a longer idle interval when all apps are stable.
 */

import type { HomeData } from '$lib/types';

const IDLE_INTERVAL_MS = 10_000;
const ACTIVE_INTERVAL_MS = 2_000;

function isTransitioning(app: { status: string }): boolean {
	return ['installing', 'starting', 'uninstalling'].includes(app.status);
}

type PollerCallback = (data: HomeData) => void;

let pollerTimeout: ReturnType<typeof setTimeout> | null = null;
let activeCallback: PollerCallback | null = null;

async function pollOnce(): Promise<void> {
	if (!activeCallback) return;
	try {
		const res = await fetch('/api/user/home');
		if (res.status === 401) {
			stopPolling();
			return;
		}
		if (!res.ok) {
			scheduleNext(IDLE_INTERVAL_MS);
			return;
		}
		const data: HomeData = await res.json();
		activeCallback(data);
		const active = data.apps.some(isTransitioning);
		scheduleNext(active ? ACTIVE_INTERVAL_MS : IDLE_INTERVAL_MS);
	} catch {
		scheduleNext(IDLE_INTERVAL_MS);
	}
}

function scheduleNext(delay: number): void {
	if (pollerTimeout) clearTimeout(pollerTimeout);
	pollerTimeout = setTimeout(pollOnce, delay);
}

export function startPolling(cb: PollerCallback): void {
	stopPolling();
	activeCallback = cb;
	pollOnce();
}

export function stopPolling(): void {
	if (pollerTimeout) {
		clearTimeout(pollerTimeout);
		pollerTimeout = null;
	}
	activeCallback = null;
}
