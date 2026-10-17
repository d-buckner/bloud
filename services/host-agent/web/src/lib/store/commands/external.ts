// SPDX-License-Identifier: AGPL-3.0-only
/**
 * External app commands - launchers and remote providers.
 *
 * These write only. The list they belong to is read by the page that shows it,
 * and this pass does not move that read into the store, so there is no confirmed
 * value to project an overlay onto. The command layer still earns its place here:
 * a double submit is one request, a failure is one toast with the right words,
 * and the call site does not talk to the client module itself.
 */

import {
	addExternalApp as apiAddLauncher,
	addExternalProvider as apiAddProvider,
	removeExternalApp as apiRemove,
	updateExternalApp as apiUpdate
} from '$lib/clients/settingsClient';
import type { ExternalAppUpdate } from '$lib/clients/settingsClient';
import type { IntentResponse } from '$lib/types';
import { runCommand } from '../runner';
import type { CommandResult } from '../runner';
import { commandKey } from '../reducers/commands';

export function addLauncher(input: {
	name: string;
	url: string;
	icon: string;
}): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('external', 'add:launcher'),
		failure: 'Could not add the shortcut',
		run: () => apiAddLauncher(input)
	});
}

export function addProvider(input: {
	app: string;
	name: string;
	url: string;
	icon?: string;
	values: Record<string, Record<string, string>>;
	secrets: Record<string, string>;
}): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('external', `add:provider:${input.app}`),
		failure: `Could not add ${input.name}`,
		run: () => apiAddProvider(input)
	});
}

export function updateExternalApp(id: string, patch: ExternalAppUpdate): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('external', `update:${id}`),
		failure: 'Could not save the change',
		run: () => apiUpdate(id, patch)
	});
}

export function removeExternalApp(id: string): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('external', `remove:${id}`),
		failure: 'Could not remove it',
		run: () => apiRemove(id)
	});
}
