// SPDX-License-Identifier: AGPL-3.0-only
/**
 * App lifecycle actions, scoped to the components that own them.
 *
 * A component never imports the command module. It imports the actions its own
 * job needs, so the catalog can install and the grid cannot, and a settings form
 * has no way to reach either.
 */

import type { App, IntentResponse } from '$lib/types';
import { commands } from '../commands';
import type { CommandResult } from '../runner';

export interface AppActions {
	install(name: string): Promise<CommandResult<App>>;
	uninstall(name: string): Promise<CommandResult<IntentResponse>>;
	rename(name: string, displayName: string): Promise<CommandResult<IntentResponse>>;
}

const ACTIONS: AppActions = {
	install: (name) => commands.installApp(name),
	uninstall: (name) => commands.uninstallApp(name),
	rename: (name, displayName) => commands.renameApp(name, displayName)
};

export function useAppActions(): AppActions {
	return ACTIONS;
}
