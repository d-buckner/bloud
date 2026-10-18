// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The root reducer: every slice, in one object, so the shape of the dashboard's
 * whole state is readable from one file.
 */

import { combineReducers } from 'redux';
import { apps } from './apps';
import { commands } from './commands';
import { connection } from './connection';
import { graph } from './graph';
import { launchers } from './launchers';
import { layout } from './layout';
import { progress } from './progress';
import { session } from './session';
import { settings } from './settings';
import { toasts } from './toasts';

export const rootReducer = combineReducers({
	session,
	connection,
	apps,
	layout,
	launchers,
	graph,
	progress,
	settings,
	toasts,
	commands
});

export type RootState = ReturnType<typeof rootReducer>;
