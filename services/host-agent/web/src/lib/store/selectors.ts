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
 * A selector takes the whole state and returns a value. It never stores one,
 * except for the identity cache `memo` keeps, which returns the same value
 * rather than a new equal one.
 */

import type { App } from '$lib/types';
import type { RootState } from './reducers';
import { isGridApp } from './reducers/apps';
import type { AppProgress } from './reducers/progress';
import { commandKey, isPending } from './reducers/commands';
import { memo, memoBy } from './memo';
import { isStale } from './reducers/graph';
import type { GraphState } from './reducers/graph';

/** The apps that get a tile, in snapshot order. */
export const gridApps = memo<App[], RootState>(
	(s) => [s.apps],
	(state) =>
		state.apps.order
			.map((id) => state.apps.byId[id])
			.filter((app): app is App => !!app && isGridApp(app))
);

/** Every installed app, including system and headless ones. */
export const allApps = memo<App[], RootState>(
	(s) => [s.apps],
	(state) => state.apps.order.map((id) => state.apps.byId[id]).filter((app): app is App => !!app)
);

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

export const appView = memoBy<string, AppView, RootState>(
	(state, catalogId) => [
		state.apps.byId[catalogId],
		state.progress.byId[catalogId],
		state.commands.byKey[commandKey('install', catalogId)],
		state.commands.byKey[commandKey('uninstall', catalogId)]
	],
	(state, catalogId) => {
		const app = state.apps.byId[catalogId];
		return {
			app,
			progress: state.progress.byId[catalogId] ?? null,
			installing: app?.status === 'installing' || app?.status === 'starting',
			pending:
				isPending(state.commands, commandKey('install', catalogId)) ||
				isPending(state.commands, commandKey('uninstall', catalogId))
		};
	}
);

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

export const enabledWidgetIds = memo<string[], RootState>(
	(s) => [s.layout],
	(state) => state.layout.elements.filter((el) => el.type === 'widget').map((el) => el.id)
);

/** What the home page says in its subtitle, in one place. */
export interface HomeSummary {
	apps: number;
	launchers: number;
	widgets: number;
	empty: boolean;
}

export const homeSummary = memo<HomeSummary, RootState>(
	(s) => [s.apps, s.launchers, s.layout],
	(state) => {
		const apps = gridApps(state).length;
		const launchers = state.launchers.items.length;
		const widgets = enabledWidgetIds(state).length;
		return { apps, launchers, widgets, empty: apps + launchers + widgets === 0 };
	}
);

/** Is the dashboard still waiting for its first snapshot? */
export function homeLoading(state: RootState): boolean {
	return state.apps.loading || state.connection.phase === 'bootstrapping';
}

/**
 * Everything the developer page renders, in one read: the payload the canvas and
 * the table are built from, whether anything has landed yet, and whether the last
 * read failed. `stale` is the case the old page could not express, because a
 * failed poll over a good payload set `error` and kept drawing, and nothing told
 * the reader the picture had stopped moving.
 */
export interface GraphView {
	graph: GraphState;
	loaded: boolean;
	stale: boolean;
	error: string;
}

export const graphView = memo<GraphView, RootState>(
	(s) => [s.graph],
	(state) => ({
		graph: state.graph,
		loaded: state.graph.loaded,
		stale: isStale(state.graph),
		error: state.graph.error
	})
);

/** The admin gate, as state rather than as an `onMount` redirect. */
export function isAdmin(state: RootState): boolean {
	return state.session.user?.role === 'admin';
}
