// SPDX-License-Identifier: AGPL-3.0-only
/**
 * External app actions: launchers and remote providers.
 *
 * These write only. The list they belong to is read by the page that shows it,
 * so there is no resource hook for it yet, and these four methods are the whole
 * write surface a modal may reach.
 */

import type { ExternalAppUpdate } from '$lib/clients/settingsClient';
import type { IntentResponse } from '$lib/types';
import { commands } from '../commands';
import type { CommandResult } from '../runner';

export interface LauncherInput {
	name: string;
	url: string;
	icon: string;
}

export interface ProviderInput {
	app: string;
	name: string;
	url: string;
	icon?: string;
	values: Record<string, Record<string, string>>;
	secrets: Record<string, string>;
}

export interface ExternalAppActions {
	addLauncher(input: LauncherInput): Promise<CommandResult<IntentResponse>>;
	addProvider(input: ProviderInput): Promise<CommandResult<IntentResponse>>;
	update(id: string, patch: ExternalAppUpdate): Promise<CommandResult<IntentResponse>>;
	remove(id: string): Promise<CommandResult<IntentResponse>>;
}

const ACTIONS: ExternalAppActions = {
	addLauncher: (input) => commands.addLauncher(input),
	addProvider: (input) => commands.addProvider(input),
	update: (id, patch) => commands.updateExternalApp(id, patch),
	remove: (id) => commands.removeExternalApp(id)
};

export function useExternalAppActions(): ExternalAppActions {
	return ACTIONS;
}
