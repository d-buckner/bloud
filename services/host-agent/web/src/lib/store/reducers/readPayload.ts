// SPDX-License-Identifier: AGPL-3.0-only
/**
 * `readPayload` - one narrowing helper for every reducer.
 *
 * An action creator always attaches a payload, so a reducer that finds none is
 * looking at an action it did not expect. Failing loudly here beats a silent
 * `undefined` surfacing three frames later as a blank panel.
 */

import type { Action } from '../actions';

export function readPayload<T>(action: Action): T {
	const payload = (action as { payload?: T }).payload;
	if (payload === undefined) {
		throw new Error(`action ${action.type} carries no payload`);
	}
	return payload;
}
