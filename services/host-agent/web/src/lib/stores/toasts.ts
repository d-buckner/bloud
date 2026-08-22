// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Toast store - ephemeral notifications for app state transitions.
 *
 * `detectToasts` is pure (previous statuses + new snapshot → toasts) so the
 * transition logic is unit testable; the event client calls it on every
 * snapshot and pushes the result here.
 */

import { writable } from 'svelte/store';
import type { App } from '$lib/types';

export type ToastTone = 'success' | 'error' | 'info';

export interface Toast {
	id: number;
	tone: ToastTone;
	message: string;
}

const TOAST_TTL_MS = 5_000;
let nextId = 1;

export const toasts = writable<Toast[]>([]);

/** Push a toast and auto-dismiss it after the TTL. */
export function pushToast(message: string, tone: ToastTone = 'info'): void {
	const id = nextId++;
	toasts.update((current) => [...current, { id, tone, message }]);
	setTimeout(() => {
		toasts.update((current) => current.filter((t) => t.id !== id));
	}, TOAST_TTL_MS);
}

/**
 * Detect user-visible transitions between two snapshots:
 *  - app reached 'running' from a non-running status → "X is ready"
 *  - app entered 'failed' → "X failed — view details"
 * 'error' (degraded, auto-retrying) deliberately produces no toast; the tile
 * shows the state without interrupting.
 */
export function detectToasts(
	prev: Map<string, string>,
	apps: App[]
): { message: string; tone: ToastTone }[] {
	const result: { message: string; tone: ToastTone }[] = [];
	for (const app of apps) {
		const before = prev.get(app.catalog_id);
		if (app.status === 'running' && before && before !== 'running') {
			result.push({ message: `${app.display_name} is ready`, tone: 'success' });
		}
		if (app.status === 'failed' && before !== 'failed') {
			result.push({ message: `${app.display_name} failed — view details`, tone: 'error' });
		}
	}
	return result;
}
