// SPDX-License-Identifier: AGPL-3.0-only
/**
 * App commands - install, uninstall, rename.
 *
 * These replace `clients/appFacade.ts`. All three are `request`-bound: the 202
 * is the whole answer the server can give. Whether the app came up is the
 * progress slice's story, told by the stream, and treating a 202 as a running
 * app is the same mistake the AI settings save makes.
 *
 * The optimistic writes here are the narrow kind: an install adds the record the
 * 202 actually carried, and an uninstall marks the app as leaving until the next
 * snapshot says it is gone. An uninstall that the server refuses puts the status
 * back, because a tile that says "uninstalling" forever is worse than the half
 * second it would take to say "still here".
 */

import type { App } from '$lib/types';
import { installApp as apiInstall, renameApp as apiRename, uninstallApp as apiUninstall } from '$lib/clients/appClient';
import { runCommand } from '../runner';
import type { CommandResult } from '../runner';
import { commandKey } from '../reducers/commands';
import { appsStatusPatched, appsUpserted, layoutElementAdded } from '../actions';
import { dispatch, getState } from '../kernel';
import { appElement } from '../reducers/layout';

/** Install an app. The tile appears from the 202's record, not from a guess. */
export function installApp(name: string): Promise<CommandResult<null>> {
	return runCommand<null>({
		key: commandKey('install', name),
		kind: 'request',
		run: async () => {
			const res = await apiInstall(name);
			const entry = res.app ?? syntheticInstalling(name);
			dispatch(appsUpserted(entry));
			// A headless app gets no tile, so no optimistic add: adding one would
			// put a tile on screen and take it away again on the next snapshot.
			if (!entry.headless) dispatch(layoutElementAdded(appElement(entry.catalog_id)));
			return null;
		}
	});
}

/** Uninstall an app, marking it gone as far as the grid is concerned. */
export function uninstallApp(name: string): Promise<CommandResult<null>> {
	return runCommand<null>({
		key: commandKey('uninstall', name),
		kind: 'request',
		run: async () => {
			const before = getState().apps.byId[name]?.status;
			dispatch(appsStatusPatched({ catalogId: name, status: 'uninstalling' }));
			try {
				await apiUninstall(name);
			} catch (err) {
				if (before) dispatch(appsStatusPatched({ catalogId: name, status: before }));
				throw err;
			}
			return null;
		}
	});
}

/** Rename an app's display name. */
export function renameApp(name: string, displayName: string): Promise<CommandResult<null>> {
	return runCommand<null>({
		key: commandKey('rename', name),
		kind: 'request',
		run: async () => {
			await apiRename(name, displayName);
			return null;
		}
	});
}

/** An install the orchestrator accepted before the row was readable. */
function syntheticInstalling(name: string): App {
	const now = new Date().toISOString();
	return {
		id: 0,
		catalog_id: name,
		display_name: name,
		version: '',
		status: 'installing',
		is_system: false,
		installed_at: now,
		updated_at: now
	};
}
