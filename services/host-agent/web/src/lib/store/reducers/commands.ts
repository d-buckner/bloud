// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Commands slice - what the user asked for that has not finished yet.
 *
 * Nothing in the dashboard today can answer "is a save in flight?" without a
 * component-local boolean, and the two places that do have one get it wrong:
 * `AISettingsSection.handleSave` treats a 202 as completion, and
 * `AddressSettingsSection` hand-rolls a convergence poll. Both are the same
 * missing concept: a write whose completion is not the response.
 *
 * A command is keyed (`install:jellyfin`), and the key is the dedupe key and the
 * store key at the same time.
 *
 * The record also carries the optimistic patch, and that placement is the whole
 * overlay lifetime. A patch is projected while its command is pending, and a
 * terminal status drops it in the same reducer case that records the outcome.
 * There is no rollback path to forget, because no confirmed value was touched.
 */

import {
	COMMAND_FAILED,
	COMMAND_REFINED,
	COMMAND_SETTLED,
	COMMAND_STARTED,
	COMMAND_SUPERSEDED,
	COMMANDS_ABANDONED
} from '../actions';
import type { Action } from '../actions';
import type { OptimisticPatch } from '../overlay';
import { readPayload } from './readPayload';

/**
 * What has to be true before a command counts as done.
 *  - `request`: the server accepted it. The response is the whole answer.
 *  - `reread`: the server accepted it and the read that shows it came back.
 *  - `convergence`: the orchestrator applied it, which no response can say.
 */
export type SettleKind = 'request' | 'reread' | 'convergence';

/**
 *  - `pending`: in flight, and its overlay is projected.
 *  - `settled`: done, and forgotten except in devtools.
 *  - `failed`: rejected, or the read never confirmed it.
 *  - `superseded`: a newer command with the same key replaced it. It is not a
 *    failure, so it raises no toast and it drops its overlay silently.
 */
export type CommandStatus = 'pending' | 'settled' | 'failed' | 'superseded';

export type CommandKey = string;

export interface CommandRecord {
	key: CommandKey;
	kind: SettleKind;
	status: CommandStatus;
	startedAt: number;
	settledAt: number;
	error: string;
	/**
	 * The claim to show over confirmed state while this is pending. Dropped at a
	 * terminal status.
	 */
	optimistic?: OptimisticPatch;
}

export interface CommandsState {
	byKey: Record<CommandKey, CommandRecord>;
}

export const initialCommands: CommandsState = { byKey: {} };

/** The key shape, in one place, so two call sites cannot mean two operations. */
export function commandKey(kind: string, target: string): CommandKey {
	return `${kind}:${target}`;
}

export function commands(state = initialCommands, action: Action): CommandsState {
	switch (action.type) {
		case COMMAND_STARTED:
			return started(state, action);
		case COMMAND_REFINED:
			return refined(state, action);
		case COMMAND_SETTLED:
			return ended(state, action, 'settled');
		case COMMAND_FAILED:
			return failed(state, action);
		case COMMAND_SUPERSEDED:
			return ended(state, action, 'superseded');
		case COMMANDS_ABANDONED:
			// A module swap keeps the store but not the promises that were driving
			// it. Every pending record is now a claim nothing can confirm or drop, so
			// the overlay it projects would pin forever. Forget the set instead of
			// marking four hundred stale entries failed.
			return initialCommands;
		default:
			return state;
	}
}

function started(state: CommandsState, action: Action): CommandsState {
	const { key, kind, at, optimistic } = readPayload<{
		key: CommandKey;
		kind: SettleKind;
		at: number;
		optimistic?: OptimisticPatch;
	}>(action);
	return {
		byKey: {
			...state.byKey,
			[key]: { key, kind, status: 'pending', startedAt: at, settledAt: 0, error: '', optimistic }
		}
	};
}

/**
 * The server canonicalizes some values, so a command may replace its own patch
 * with what was actually stored. Without this the overlay can never match
 * confirmed state, and the poll runs to its cap over a save that already landed.
 */
function refined(state: CommandsState, action: Action): CommandsState {
	const { key, patch } = readPayload<{ key: CommandKey; patch: OptimisticPatch | null }>(action);
	const record = state.byKey[key];
	if (!record || record.status !== 'pending') return state;
	return { byKey: { ...state.byKey, [key]: { ...record, optimistic: patch ?? undefined } } };
}

function ended(state: CommandsState, action: Action, status: 'settled' | 'superseded'): CommandsState {
	const { key, at } = readPayload<{ key: CommandKey; at: number }>(action);
	return mark(state, key, { status, settledAt: at, error: '', optimistic: undefined });
}

function failed(state: CommandsState, action: Action): CommandsState {
	const { key, error, at } = readPayload<{ key: CommandKey; error: string; at: number }>(action);
	return mark(state, key, { status: 'failed', settledAt: at, error, optimistic: undefined });
}

/** Every terminal status drops the overlay, because the claim is no longer pending. */
function mark(state: CommandsState, key: CommandKey, patch: Partial<CommandRecord>): CommandsState {
	const record = state.byKey[key];
	if (!record) return state;
	return { byKey: { ...state.byKey, [key]: { ...record, ...patch } } };
}

/** Is anything in flight for this key? This is the only answer to "saving?". */
export function isPending(state: CommandsState, key: CommandKey): boolean {
	return state.byKey[key]?.status === 'pending';
}

/** The last outcome for a key, for a component that wants to say what happened. */
export function lastOutcome(state: CommandsState, key: CommandKey): CommandRecord | undefined {
	return state.byKey[key];
}
