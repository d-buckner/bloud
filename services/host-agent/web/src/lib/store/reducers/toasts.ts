// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Toasts slice - the notifications the dashboard raises about itself.
 *
 * Replaces `stores/toasts.ts`. The id counter used to be a module `let`, which
 * meant two stores could hand out the same id after a hot reload; it is state
 * now. What a toast is *about* stays a pure function (`detectToasts`) so the
 * transition rule is testable on its own, and the middleware in `../kernel.ts`
 * is the only thing that calls it.
 */

import type { App } from '$lib/types';
import { TOAST_DISMISSED, TOAST_PUSHED } from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

export type ToastTone = 'success' | 'error' | 'info';

export interface Toast {
	id: number;
	tone: ToastTone;
	message: string;
}

export interface ToastsState {
	items: Toast[];
	nextId: number;
}

export const initialToasts: ToastsState = { items: [], nextId: 1 };

/** A toast stays up long enough to read, then leaves on its own. */
export const TOAST_TTL_MS = 5_000;

export function toasts(state = initialToasts, action: Action): ToastsState {
	switch (action.type) {
		case TOAST_PUSHED: {
			const { tone, message } = readPayload<{ tone: ToastTone; message: string }>(action);
			return { items: [...state.items, { id: state.nextId, tone, message }], nextId: state.nextId + 1 };
		}
		case TOAST_DISMISSED: {
			const { id } = readPayload<{ id: number }>(action);
			return { ...state, items: state.items.filter((t) => t.id !== id) };
		}
		default:
			return state;
	}
}

export interface DetectedToast {
	message: string;
	tone: ToastTone;
}

/**
 * User-visible transitions between two status maps:
 *  - an app reached 'running' from a non-running status, so say it is ready
 *  - an app entered 'failed', so say it failed
 * 'error' (degraded, auto-retrying) deliberately raises nothing: the tile shows
 * that state on its own and interrupting for it would be noise.
 */
export function detectToasts(prev: Map<string, string>, apps: App[]): DetectedToast[] {
	const result: DetectedToast[] = [];
	for (const app of apps) {
		const before = prev.get(app.catalog_id);
		if (app.status === 'running' && before && before !== 'running') {
			result.push({ message: `${app.display_name} is ready`, tone: 'success' });
		}
		if (app.status === 'failed' && before !== 'failed') {
			result.push({ message: `${app.display_name} failed: view details`, tone: 'error' });
		}
	}
	return result;
}
