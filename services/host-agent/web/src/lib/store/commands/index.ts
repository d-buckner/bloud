// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The command module's public surface: every write the dashboard can make.
 *
 * A component imports this object and calls a named operation. It never imports
 * the runner, a client module, or a settle kind. That is the depth of the module:
 * one list of operations, and all the orchestration knowledge (dedupe keys,
 * settle kinds, overlays, poll windows, toast wording) is behind it.
 *
 * `scripts/no-component-writes.mjs` enforces the other half of the claim: a
 * mutating client call outside `store/commands/` fails the fast tier.
 */

import { bootstrapSession, signOut, startDashboard } from './session';
import { installApp, renameApp, uninstallApp } from './apps';
import { removeWidget, saveLayout, toggleWidget } from './layout';
import {
	loadAISettings,
	loadEndpoint,
	loadPublicURL,
	loadTimezone,
	removeEndpoint,
	saveAISettings,
	saveEndpoint,
	savePublicURL,
	saveTimezone
} from './settings';
import { createUser, deleteUser, loadUsers, setUserRole } from './users';
import { addLauncher, addProvider, removeExternalApp, updateExternalApp } from './external';

export const commands = {
	// Session
	bootstrapSession,
	signOut,
	startDashboard,

	// Apps
	installApp,
	uninstallApp,
	renameApp,

	// Grid
	saveLayout,
	toggleWidget,
	removeWidget,

	// Settings
	loadPublicURL,
	savePublicURL,
	loadTimezone,
	saveTimezone,
	loadAISettings,
	saveAISettings,
	loadEndpoint,
	saveEndpoint,
	removeEndpoint,

	// Users
	loadUsers,
	createUser,
	deleteUser,
	setUserRole,

	// External apps
	addLauncher,
	addProvider,
	updateExternalApp,
	removeExternalApp
} as const;

export type Commands = typeof commands;
