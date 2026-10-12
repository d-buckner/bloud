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
 * A command is keyed (`install:jellyfin`), and the key is what makes a double
 * click one operation instead of two.
 */

import { COMMAND_FAILED, COMMAND_SETTLED, COMMAND_STARTED } from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

/**
 * What has to be true before a command counts as done.
 *  - `request`: the server accepted it. The response is the whole answer.
 *  - `reread`: the server accepted it and the read that shows it came back.
 *  - `convergence`: the orchestrator applied it, which no response can say.
 */
export type SettleKind = 'request' | 'reread' | 'convergence';

export type CommandStatus = 'pending' | 'settled' | 'failed';

export type CommandKey = string;

export interface CommandRecord {
	key: CommandKey;
	kind: SettleKind;
	status: CommandStatus;
	startedAt: number;
	settledAt: number;
	error: string;
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
		case COMMAND_STARTED: {
			const { key, kind, at } = readPayload<{ key: CommandKey; kind: SettleKind; at: number }>(action);
			return {
				byKey: {
					...state.byKey,
					[key]: { key, kind, status: 'pending', startedAt: at, settledAt: 0, error: '' }
				}
			};
		}
		case COMMAND_SETTLED: {
			const { key, at } = readPayload<{ key: CommandKey; at: number }>(action);
			const record = state.byKey[key];
			if (!record) return state;
			return { byKey: { ...state.byKey, [key]: { ...record, status: 'settled', settledAt: at, error: '' } } };
		}
		case COMMAND_FAILED: {
			const { key, error, at } = readPayload<{ key: CommandKey; error: string; at: number }>(action);
			const record = state.byKey[key];
			if (!record) return state;
			return { byKey: { ...state.byKey, [key]: { ...record, status: 'failed', settledAt: at, error } } };
		}
		default:
			return state;
	}
}

/** Is anything in flight for this key? This is the only answer to "saving?". */
export function isPending(state: CommandsState, key: CommandKey): boolean {
	return state.byKey[key]?.status === 'pending';
}

