// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Launchers slice - operator-declared tiles that open a URL.
 *
 * Replaces `stores/launchers.ts`. The records live here and their grid positions
 * live in the layout slice, which is the same split the server makes: the
 * launcher is the thing, the position is where this user put it.
 */

import type { HomeData, Launcher } from '$lib/types';
import { HOME_SNAPSHOT, LAUNCHERS_REPLACED } from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

export interface LaunchersState {
	items: Launcher[];
}

export const initialLaunchers: LaunchersState = { items: [] };

export function launchers(state = initialLaunchers, action: Action): LaunchersState {
	switch (action.type) {
		case HOME_SNAPSHOT:
			return { items: readPayload<HomeData>(action).launchers ?? [] };
		case LAUNCHERS_REPLACED:
			return { items: readPayload<Launcher[]>(action) };
		default:
			return state;
	}
}
