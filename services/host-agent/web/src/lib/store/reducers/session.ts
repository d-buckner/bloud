// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Session slice - who is looking at this dashboard, and whether we know yet.
 *
 * `bootstrapping` is a real phase rather than a `loading` boolean scattered
 * across components, because the difference between "not asked yet", "asked and
 * anonymous", and "asked and the API is down" is the difference between a
 * spinner, a redirect to login, and an error state. `+layout.svelte` today
 * collapses all three into `loading` plus a `catch` that assumes dev mode.
 */

import {
	SESSION_BOOTSTRAP_FAILED,
	SESSION_BOOTSTRAP_LOADED,
	SESSION_BOOTSTRAP_STARTED,
	SESSION_SIGNED_OUT
} from '../actions';
import type { Action, SessionUser } from '../actions';
import { readPayload } from './readPayload';

export type SessionStatus = 'bootstrapping' | 'anonymous' | 'ready' | 'unreachable';

export interface SessionState {
	status: SessionStatus;
	user: SessionUser | null;
	setupRequired: boolean;
	error: string | null;
}

export const initialSession: SessionState = {
	status: 'bootstrapping',
	user: null,
	setupRequired: false,
	error: null
};

export function session(state = initialSession, action: Action): SessionState {
	switch (action.type) {
		case SESSION_BOOTSTRAP_STARTED:
			return { ...state, status: 'bootstrapping', error: null };
		case SESSION_BOOTSTRAP_LOADED: {
			const payload = readPayload<{ user: SessionUser | null; setupRequired: boolean }>(action);
			return {
				status: payload.user ? 'ready' : 'anonymous',
				user: payload.user,
				setupRequired: payload.setupRequired,
				error: null
			};
		}
		case SESSION_BOOTSTRAP_FAILED: {
			const payload = readPayload<{ message: string }>(action);
			return { ...state, status: 'unreachable', error: payload.message };
		}
		case SESSION_SIGNED_OUT:
			return { ...initialSession, status: 'anonymous' };
		default:
			return state;
	}
}
