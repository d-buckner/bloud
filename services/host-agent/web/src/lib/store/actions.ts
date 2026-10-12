// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Actions - the only way anything enters the store.
 *
 * Reducers are the only writers, and an action is the only thing a reducer
 * reads besides its own slice. Anything that used to be `store.set(...)` from a
 * client module is an action creator here instead, so the whole set of things
 * that can change the dashboard is one readable list.
 */

import type { App, GridElement, HomeData, Launcher, Role } from '$lib/types';
import type { ActivityEvent, NodeEvent, PullEvent } from './wire';
import type { ConnectionEvent } from './machine';
import type { ToastTone } from './reducers/toasts';
import type { CommandKey, SettleKind } from './reducers/commands';

export const SESSION_BOOTSTRAP_STARTED = 'session/bootstrapStarted';
export const SESSION_BOOTSTRAP_LOADED = 'session/bootstrapLoaded';
export const SESSION_BOOTSTRAP_FAILED = 'session/bootstrapFailed';
export const SESSION_SIGNED_OUT = 'session/signedOut';

export const CONNECTION_EVENT = 'connection/event';
export const CONNECTION_RESET = 'connection/reset';

export const HOME_SNAPSHOT = 'home/snapshot';

export const APPS_UPSERTED = 'apps/upserted';
export const APPS_STATUS_PATCHED = 'apps/statusPatched';
export const APPS_READ_FAILED = 'apps/readFailed';

export const LAYOUT_ELEMENT_ADDED = 'layout/elementAdded';
export const LAYOUT_ELEMENT_REMOVED = 'layout/elementRemoved';
export const LAYOUT_POSITIONS_SET = 'layout/positionsSet';

export const LAUNCHERS_REPLACED = 'launchers/replaced';

export const PROGRESS_NODE = 'progress/node';
export const PROGRESS_PULL = 'progress/pull';
export const PROGRESS_ACTIVITY = 'progress/activity';

export const TOAST_PUSHED = 'toast/pushed';
export const TOAST_DISMISSED = 'toast/dismissed';

export const COMMAND_STARTED = 'command/started';
export const COMMAND_SETTLED = 'command/settled';
export const COMMAND_FAILED = 'command/failed';

/** Re-apply the last snapshot so an edited reducer shows up without a reload. */
export const DEV_RESYNC = 'dev/resync';

export interface SessionUser {
	username: string;
	role: Role;
}

/**
 * The action shape. The index signature is not decoration: redux 5's
 * `UnknownAction` requires one, and without it every `dispatch` call site fails
 * to typecheck against the store. Payloads stay typed by the creators below.
 */
export interface Action {
	type: string;
	payload?: unknown;
	[key: string]: unknown;
}

export function action<T extends string, P>(type: T, payload: P): Action & { payload: P } {
	return { type, payload };
}

/**
 * Every action type the store accepts, as data.
 *
 * `Action` is structurally `{ type: string }`, so the action union is a
 * convention rather than something the compiler enforces: a typo'd
 * `'apss/upserted'` compiles, falls through every reducer's `default`, and
 * changes nothing. That is the worst kind of typo to chase, because the symptom
 * is a UI that quietly never updates.
 *
 * So the kernel refuses a type that is not in this list. Adding an action means
 * adding it here too, which is the same "the list is the rule" shape as the
 * `EXCEPTIONS` tables under `scripts/`.
 */
export const KNOWN_ACTION_TYPES: readonly string[] = Object.freeze([
	SESSION_BOOTSTRAP_STARTED,
	SESSION_BOOTSTRAP_LOADED,
	SESSION_BOOTSTRAP_FAILED,
	SESSION_SIGNED_OUT,
	CONNECTION_EVENT,
	CONNECTION_RESET,
	HOME_SNAPSHOT,
	APPS_UPSERTED,
	APPS_STATUS_PATCHED,
	APPS_READ_FAILED,
	LAYOUT_ELEMENT_ADDED,
	LAYOUT_ELEMENT_REMOVED,
	LAYOUT_POSITIONS_SET,
	LAUNCHERS_REPLACED,
	PROGRESS_NODE,
	PROGRESS_PULL,
	PROGRESS_ACTIVITY,
	TOAST_PUSHED,
	TOAST_DISMISSED,
	COMMAND_STARTED,
	COMMAND_SETTLED,
	COMMAND_FAILED,
	DEV_RESYNC,
]);

const KNOWN = new Set(KNOWN_ACTION_TYPES);

/** Is this a type the store has a reducer for? */
export function isKnownAction(type: string): boolean {
	return KNOWN.has(type);
}

export const bootstrapStarted = () => action(SESSION_BOOTSTRAP_STARTED, null);

export const bootstrapLoaded = (payload: { user: SessionUser | null; setupRequired: boolean }) =>
	action(SESSION_BOOTSTRAP_LOADED, payload);

export const bootstrapFailed = (payload: { message: string }) => action(SESSION_BOOTSTRAP_FAILED, payload);

export const signedOut = () => action(SESSION_SIGNED_OUT, null);

export const connectionEvent = (payload: ConnectionEvent) => action(CONNECTION_EVENT, payload);
export const connectionReset = () => action(CONNECTION_RESET, null);

export const homeSnapshot = (payload: HomeData) => action(HOME_SNAPSHOT, payload);

export const appsUpserted = (payload: App) => action(APPS_UPSERTED, payload);
export const appsStatusPatched = (payload: { catalogId: string; status: App['status'] }) =>
	action(APPS_STATUS_PATCHED, payload);
export const appsReadFailed = (payload: { message: string }) => action(APPS_READ_FAILED, payload);

export const layoutElementAdded = (payload: GridElement) => action(LAYOUT_ELEMENT_ADDED, payload);
export const layoutElementRemoved = (payload: { type: GridElement['type']; id: string }) =>
	action(LAYOUT_ELEMENT_REMOVED, payload);
export const layoutPositionsSet = (payload: GridElement[]) => action(LAYOUT_POSITIONS_SET, payload);

export const progressNode = (payload: NodeEvent) => action(PROGRESS_NODE, payload);
export const progressPull = (payload: PullEvent) => action(PROGRESS_PULL, payload);
export const progressActivity = (payload: ActivityEvent) => action(PROGRESS_ACTIVITY, payload);

export const toastPushed = (payload: { tone: ToastTone; message: string }) => action(TOAST_PUSHED, payload);
export const toastDismissed = (payload: { id: number }) => action(TOAST_DISMISSED, payload);

export const commandStarted = (payload: { key: CommandKey; kind: SettleKind; at: number }) =>
	action(COMMAND_STARTED, payload);
export const commandSettled = (payload: { key: CommandKey; at: number }) => action(COMMAND_SETTLED, payload);
export const commandFailed = (payload: { key: CommandKey; error: string; at: number }) =>
	action(COMMAND_FAILED, payload);

export const devResync = (payload: HomeData) => action(DEV_RESYNC, payload);

export const launchersReplaced = (payload: Launcher[]) => action(LAUNCHERS_REPLACED, payload);
