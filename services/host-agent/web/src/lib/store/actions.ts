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
import type { SettingsField, SettingsLoadedPayload } from './reducers/settings';
import type { OptimisticPatch } from './overlay';
import type { DeveloperGraph } from '$lib/clients/developerClient';

// The type strings live in `actionTypes.ts` so the known-type set below can be
// derived from them instead of transcribed. Re-exported here because every
// reducer and creator imports from this module.
export * from './actionTypes';
import * as actionTypes from './actionTypes';

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
 * Every action type the store accepts, derived from the module that defines
 * them. Nothing to keep in sync, because there is only one list.
 *
 * `Action` is structurally `{ type: string }`, so the action union is a
 * convention rather than something the compiler enforces: a typo'd
 * `'apss/upserted'` compiles, falls through every reducer's `default`, and
 * changes nothing. That is the worst kind of typo to chase, because the symptom
 * is a UI that quietly never updates. So the store refuses a type that is not
 * here.
 */
export const KNOWN_ACTION_TYPES: readonly string[] = Object.freeze(
	Object.values(actionTypes)
);

const KNOWN = new Set(KNOWN_ACTION_TYPES);

/** Is this a type the store has a reducer for? */
export function isKnownAction(type: string): boolean {
	return KNOWN.has(type);
}

export const bootstrapStarted = () => action(actionTypes.SESSION_BOOTSTRAP_STARTED, null);

export const bootstrapLoaded = (payload: { user: SessionUser | null; setupRequired: boolean }) =>
	action(actionTypes.SESSION_BOOTSTRAP_LOADED, payload);

export const bootstrapFailed = (payload: { message: string }) => action(actionTypes.SESSION_BOOTSTRAP_FAILED, payload);

export const signedOut = () => action(actionTypes.SESSION_SIGNED_OUT, null);

export const connectionEvent = (payload: ConnectionEvent) => action(actionTypes.CONNECTION_EVENT, payload);
export const connectionReset = () => action(actionTypes.CONNECTION_RESET, null);

export const homeSnapshot = (payload: HomeData) => action(actionTypes.HOME_SNAPSHOT, payload);

export const appsUpserted = (payload: App) => action(actionTypes.APPS_UPSERTED, payload);
export const appsStatusPatched = (payload: { catalogId: string; status: App['status'] }) =>
	action(actionTypes.APPS_STATUS_PATCHED, payload);
export const appsReadFailed = (payload: { message: string }) => action(actionTypes.APPS_READ_FAILED, payload);

export const layoutElementAdded = (payload: GridElement) => action(actionTypes.LAYOUT_ELEMENT_ADDED, payload);
export const layoutElementRemoved = (payload: { type: GridElement['type']; id: string }) =>
	action(actionTypes.LAYOUT_ELEMENT_REMOVED, payload);
export const layoutPositionsSet = (payload: GridElement[]) => action(actionTypes.LAYOUT_POSITIONS_SET, payload);

export const progressNode = (payload: NodeEvent) => action(actionTypes.PROGRESS_NODE, payload);
export const progressPull = (payload: PullEvent) => action(actionTypes.PROGRESS_PULL, payload);
export const progressActivity = (payload: ActivityEvent) => action(actionTypes.PROGRESS_ACTIVITY, payload);

export const toastPushed = (payload: { tone: ToastTone; message: string }) => action(actionTypes.TOAST_PUSHED, payload);
export const toastDismissed = (payload: { id: number }) => action(actionTypes.TOAST_DISMISSED, payload);

export const commandStarted = (payload: {
	key: CommandKey;
	kind: SettleKind;
	at: number;
	optimistic?: OptimisticPatch;
}) => action(actionTypes.COMMAND_STARTED, payload);
export const commandRefined = (payload: { key: CommandKey; patch: OptimisticPatch | null }) =>
	action(actionTypes.COMMAND_REFINED, payload);
export const commandSettled = (payload: { key: CommandKey; at: number }) =>
	action(actionTypes.COMMAND_SETTLED, payload);
export const commandFailed = (payload: { key: CommandKey; error: string; at: number }) =>
	action(actionTypes.COMMAND_FAILED, payload);
export const commandSuperseded = (payload: { key: CommandKey; at: number }) =>
	action(actionTypes.COMMAND_SUPERSEDED, payload);
export const commandsAbandoned = () => action(actionTypes.COMMANDS_ABANDONED, null);

export const settingsReadStarted = (payload: { field: SettingsField }) =>
	action(actionTypes.SETTINGS_READ_STARTED, payload);
export const settingsLoaded = (payload: SettingsLoadedPayload) =>
	action(actionTypes.SETTINGS_LOADED, payload);
export const settingsReadFailed = (payload: { field: SettingsField; error: string }) =>
	action(actionTypes.SETTINGS_READ_FAILED, payload);

export const launchersReplaced = (payload: Launcher[]) => action(actionTypes.LAUNCHERS_REPLACED, payload);

/** A developer-graph read landed. Carries the clock so the reducer stays pure. */
export const graphLoaded = (payload: DeveloperGraph, at: number) =>
	action(actionTypes.GRAPH_LOADED, { payload, at });
export const graphFailed = (error: string) => action(actionTypes.GRAPH_FAILED, { error });
export const graphReset = () => action(actionTypes.GRAPH_RESET, null);
