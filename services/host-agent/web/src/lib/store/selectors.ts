// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Selectors - the places where two slices become one answer.
 *
 * Every rule here used to live in a component: which apps get a tile, what a
 * click on a tile means, how to count what is on the grid. They are rules about
 * the domain rather than about a tile, and while they lived in the page they could
 * not be tested, could not be reused, and drifted between the two pages that each
 * had a copy.
 *
 * A selector takes the whole state and returns a value. It never stores one.
 */

import type { App } from '$lib/types';
import type { RootState } from './reducers';
import { isGridApp } from './reducers/apps';
import type { AppProgress } from './reducers/progress';
import { commandKey, isPending } from './reducers/commands';

/** The apps that get a tile, in snapshot order. */
export function gridApps(state: RootState): App[] {
	return state.apps.order.map((id) => state.apps.byId[id]).filter((app): app is App => !!app && isGridApp(app));
}

/** Every installed app, including system and headless ones. */
export function allApps(state: RootState): App[] {
	return state.apps.order.map((id) => state.apps.byId[id]).filter((app): app is App => !!app);
}

/**
 * One app plus its live progress: the join the home page and the install modal
 * each did by hand. `progress` is null until the first event for this app, which
 * is what tells a tile the difference between "queued" and "no news yet".
 */
export interface AppView {
	app: App | undefined;
	progress: AppProgress | null;
	installing: boolean;
	/** True while an install or uninstall request for this app is in flight. */
	pending: boolean;
}

export function appView(state: RootState, catalogId: string): AppView {
	const app = state.apps.byId[catalogId];
	const progress = state.progress.byId[catalogId] ?? null;
	return {
		app,
		progress,
		installing: app?.status === 'installing' || app?.status === 'starting',
		pending:
			isPending(state.commands, commandKey('install', catalogId)) ||
			isPending(state.commands, commandKey('uninstall', catalogId))
	};
}

/**
 * Can this tile be opened, or is it still coming up? The set of statuses that
 * mean "show the live view instead of launching" was a `Set` in the page; it is
 * a rule about app statuses, so it belongs here. A failed or degraded app opens
 * the progress view because investigating it is the useful thing to do.
 */
export function opensProgressInsteadOfLaunching(app: App | undefined): boolean {
	if (!app) return false;
	return (
		app.status === 'error' ||
		app.status === 'installing' ||
		app.status === 'starting' ||
		app.status === 'failed'
	);
}

export function enabledWidgetIds(state: RootState): string[] {
	return state.layout.elements.filter((el) => el.type === 'widget').map((el) => el.id);
}

/** What the home page says in its subtitle, in one place. */
export interface HomeSummary {
	apps: number;
	launchers: number;
	widgets: number;
	empty: boolean;
}

export function homeSummary(state: RootState): HomeSummary {
	const apps = gridApps(state).length;
	const launchers = state.launchers.items.length;
	const widgets = enabledWidgetIds(state).length;
	return { apps, launchers, widgets, empty: apps + launchers + widgets === 0 };
}

/** Is the dashboard still waiting for its first snapshot? */
export function homeLoading(state: RootState): boolean {
	return state.apps.loading || state.connection.phase === 'bootstrapping';
}

/** The admin gate, as state rather than as an `onMount` redirect. */
export function isAdmin(state: RootState): boolean {
	return state.session.user?.role === 'admin';
}
