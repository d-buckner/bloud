// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Settings slice - the confirmed value of every writable instance setting.
 *
 * This slice holds what the server says, and nothing else. The optimistic value
 * the user is looking at lives in `commands` and a selector projects it over
 * this. That split is what makes the settle predicate honest: a command that
 * asks "has the address changed yet?" reads this slice, so it cannot answer yes
 * with the value it just invented.
 *
 * Each setting is a `Resource`: a phase, a value, an error, and the clock of the
 * last successful read. A failed read keeps the last good value and sets the
 * phase to `error`, because a settings page that blanks itself on a blip is
 * worse than one that shows a stale value and says so.
 */

import type { AISettings, EndpointContract, EndpointSettings } from '$lib/clients/settingsClient';
import type { ManagedUser } from '$lib/clients/userClient';
import { SETTINGS_LOADED, SETTINGS_READ_FAILED, SETTINGS_READ_STARTED } from '../actions';
import type { Action } from '../actions';
import { readPayload } from './readPayload';

export type ResourcePhase = 'idle' | 'loading' | 'ready' | 'error';

export interface Resource<T> {
	phase: ResourcePhase;
	/** The confirmed value. Null means nothing has been read yet. */
	value: T | null;
	error: string;
	/** Wall clock of the last successful read. 0 means none. */
	readAt: number;
}

export function idleResource<T>(): Resource<T> {
	return { phase: 'idle', value: null, error: '', readAt: 0 };
}

/** Which resource a read or a load result belongs to. */
export type SettingsField = 'publicUrl' | 'timezone' | 'ai' | 'users' | EndpointContract;

export type SettingsLoadedPayload =
	| { field: 'publicUrl'; value: string; at: number }
	| { field: 'timezone'; value: string; at: number }
	| { field: 'ai'; value: AISettings; at: number }
	| { field: 'users'; value: ManagedUser[]; at: number }
	| { field: EndpointContract; value: EndpointSettings; at: number };

export interface SettingsState {
	publicUrl: Resource<string>;
	timezone: Resource<string>;
	ai: Resource<AISettings>;
	endpoints: Record<EndpointContract, Resource<EndpointSettings>>;
	users: Resource<ManagedUser[]>;
}

export const initialSettings: SettingsState = {
	publicUrl: idleResource<string>(),
	timezone: idleResource<string>(),
	ai: idleResource<AISettings>(),
	endpoints: {
		transcription: idleResource<EndpointSettings>(),
		embeddings: idleResource<EndpointSettings>()
	},
	users: idleResource<ManagedUser[]>()
};

/** One resource, addressed by field, for the three read actions. */
function resourceOf(state: SettingsState, field: SettingsField): Resource<unknown> {
	switch (field) {
		case 'publicUrl':
			return state.publicUrl;
		case 'timezone':
			return state.timezone;
		case 'ai':
			return state.ai;
		case 'users':
			return state.users;
		default:
			return state.endpoints[field];
	}
}

/** The same resource, replaced, back in its slice. */
function withResource(
	state: SettingsState,
	field: SettingsField,
	resource: Resource<unknown>
): SettingsState {
	switch (field) {
		case 'publicUrl':
			return { ...state, publicUrl: resource as Resource<string> };
		case 'timezone':
			return { ...state, timezone: resource as Resource<string> };
		case 'ai':
			return { ...state, ai: resource as Resource<AISettings> };
		case 'users':
			return { ...state, users: resource as Resource<ManagedUser[]> };
		default:
			return {
				...state,
				endpoints: { ...state.endpoints, [field]: resource as Resource<EndpointSettings> }
			};
	}
}

export function settings(state = initialSettings, action: Action): SettingsState {
	switch (action.type) {
		case SETTINGS_READ_STARTED: {
			const { field } = readPayload<{ field: SettingsField }>(action);
			// A read keeps the last good value while it runs: a settings page that
			// empties its fields on every refresh is a page that cannot be typed into.
			return withResource(state, field, { ...resourceOf(state, field), phase: 'loading', error: '' });
		}
		case SETTINGS_LOADED: {
			const payload = readPayload<SettingsLoadedPayload>(action);
			return withResource(state, payload.field, {
				phase: 'ready',
				value: payload.value,
				error: '',
				readAt: payload.at
			});
		}
		case SETTINGS_READ_FAILED: {
			const { field, error } = readPayload<{ field: SettingsField; error: string }>(action);
			const previous = resourceOf(state, field);
			return withResource(state, field, { ...previous, phase: 'error', error });
		}
		default:
			return state;
	}
}
