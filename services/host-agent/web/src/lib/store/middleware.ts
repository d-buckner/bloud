// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Middleware - the two places where a reaction to state belongs between the
 * dispatch and the reducers rather than inside a component.
 *
 * Both are small on purpose. This is not a middleware framework and nothing here
 * can await, retry, or chain: that is how a command layer turns into a saga
 * library nobody asked for.
 *
 *  - `toastTransitions` turns a status change in a snapshot into a toast. It sits
 *    here because the rule needs the status map from *before* the snapshot, and
 *    no slice is allowed to keep a copy of another slice's last value.
 *  - `toastTtl` schedules the dismissal. A timer that lives in a component stops
 *    existing when the component unmounts, and a toast is not the component's
 *    business.
 */

import type { Middleware, MiddlewareAPI } from 'redux';
import { HOME_SNAPSHOT, TOAST_PUSHED, toastDismissed, toastPushed } from './actions';
import type { Action } from './actions';
import { statusMap } from './reducers/apps';
import { TOAST_TTL_MS, detectToasts } from './reducers/toasts';
import type { ToastsState } from './reducers/toasts';
import type { RootState } from './reducers';

/**
 * Redux 5 types a middleware's `action` as `unknown` and its `next` as taking
 * `Action<never>`, which is the library declining to know anything about our
 * action union. This alias is the one place that assertion is made, instead of a
 * cast sprinkled through every handler.
 */
type TypedMiddleware =
	(api: MiddlewareAPI) => (next: (action: Action) => unknown) => (action: Action) => unknown;

const asMiddleware = (fn: TypedMiddleware): Middleware => fn as unknown as Middleware;

export const toastTransitions: Middleware = asMiddleware((store) => (next) => (action) => {
	if (action.type !== HOME_SNAPSHOT) return next(action);

	const state = store.getState() as RootState;
	const before = statusMap(state.apps);
	const result = next(action);
	const after = store.getState() as RootState;

	for (const toast of detectToasts(before, Object.values(after.apps.byId))) {
		store.dispatch(toastPushed(toast));
	}
	return result;
});

export const toastTtl: Middleware = asMiddleware((store) => (next) => (action) => {
	const result = next(action);
	if (action.type !== TOAST_PUSHED) return result;

	const items = (store.getState() as { toasts: ToastsState }).toasts.items;
	const newest = items[items.length - 1];
	if (newest) {
		setTimeout(() => store.dispatch(toastDismissed({ id: newest.id })), TOAST_TTL_MS);
	}
	return result;
});
