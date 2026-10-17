// SPDX-License-Identifier: AGPL-3.0-only
/**
 * App commands - install, uninstall, rename.
 *
 * All three are shown before the snapshot that proves them, because the
 * orchestrator is the only writer and its pass takes as long as it takes. The
 * overlay is what a user sees in between, and the rule that drops it is the same
 * rule the settings use: confirmed state catches up, a terminal failure drops the
 * claim, or the cap closes it.
 *
 * The confirmed read here is the stream. The home snapshot carries every app, so
 * the wait has nothing to poll: it re-checks the store on a backoff and settles
 * the moment the snapshot says the claim is true.
 */

import type { App, IntentResponse } from '$lib/types';
import { installApp as apiInstall, renameApp as apiRename, uninstallApp as apiUninstall } from '$lib/clients/appClient';
import { runCommand } from '../runner';
import type { CommandResult, PollWindow } from '../runner';
import { commandKey } from '../reducers/commands';

/**
 * The snapshot is the read, so the wait is a re-check rather than a request. The
 * interval starts slower than a settings save because a full pass is seconds at
 * best, and the cap covers a first install that pulls images.
 */
const SNAPSHOT_WINDOW: PollWindow = { firstMs: 1_000, maxMs: 5_000, capMs: 10 * 60_000 };

/** The wait re-checks state. The transport delivers the change. */
const streamIsTheRead = (): Promise<void> => Promise.resolve();

/** Install an app. The tile appears from the 202's record, not from a guess. */
export function installApp(name: string): Promise<CommandResult<App>> {
	return runCommand({
		key: commandKey('install', name),
		failure: `Could not install ${name}`,
		unconfirmed: `${name} was accepted but never came up`,
		run: async () => {
			const res = await apiInstall(name);
			return res.app ?? syntheticInstalling(name);
		},
		refine: (entry) => ({
			kind: 'apps.record',
			key: entry.catalog_id,
			value: { record: entry }
		}),
		read: streamIsTheRead,
		window: SNAPSHOT_WINDOW
	});
}

/**
 * Uninstall an app, removing its tile at once.
 *
 * The claim is a removal, so the tile goes now and the command is done when the
 * snapshot no longer has the record. That is the same rule as every other
 * convergence command: a command settles when its own claim is true.
 */
export function uninstallApp(name: string): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('uninstall', name),
		failure: `Could not uninstall ${name}`,
		unconfirmed: `${name} is still installed`,
		run: () => apiUninstall(name),
		optimistic: { kind: 'apps.removed', key: name, value: { catalogId: name } },
		read: streamIsTheRead,
		window: SNAPSHOT_WINDOW
	});
}

/** Rename an app's display name, shown before the pass that stores it. */
export function renameApp(name: string, displayName: string): Promise<CommandResult<IntentResponse>> {
	return runCommand({
		key: commandKey('rename', name),
		failure: `Could not rename ${name}`,
		unconfirmed: `The new name for ${name} is not live yet`,
		run: () => apiRename(name, displayName),
		optimistic: {
			kind: 'apps.patch',
			key: name,
			value: { catalogId: name, patch: { display_name: displayName } }
		},
		read: streamIsTheRead,
		window: SNAPSHOT_WINDOW
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
