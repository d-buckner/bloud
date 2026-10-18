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
 *
 * The settings views are also the only place an optimistic overlay reaches a
 * component. They return the pending claim over the confirmed value, and a
 * command's settle predicate never calls them: it reads the slice, which holds
 * confirmed values only.
 */

import type { App } from '$lib/types';
import type { AISettings, EndpointContract, EndpointSettings } from '$lib/clients/settingsClient';
import type { ManagedUser } from '$lib/clients/userClient';
import type { RootState } from './reducers';
import { isGridApp } from './reducers/apps';
import type { AppsState } from './reducers/apps';
import type { AppProgress } from './reducers/progress';
import { commandKey, isPending } from './reducers/commands';
import type { Resource, ResourcePhase } from './reducers/settings';
import { INSTANCE_KEY, pendingOverlay, pendingPatches, project } from './overlay';
import { memo, memoBy } from './memo';
import { isStale } from './reducers/graph';
import type { GraphState } from './reducers/graph';

/**
 * The installed apps with every pending overlay applied.
 *
 * Two passes on purpose: a record insert must land before a patch on that record,
 * or a rename on an app that is still being installed finds nothing to rename.
 * When nothing is pending the confirmed slice is returned by identity, so the
 * selectors below keep their cache.
 */
const resolvedApps = memo<AppsState, RootState>(
	(state) => [state.apps, state.commands],
	(state) => {
		const patches = pendingPatches(state.commands);
		if (patches.length === 0) return state.apps;

		let resolved = state.apps;
		for (const patch of patches) {
			if (patch.kind === 'apps.record') resolved = project('apps.record', patch.value, resolved);
		}
		for (const patch of patches) {
			if (patch.kind === 'apps.patch') resolved = project('apps.patch', patch.value, resolved);
		}
		return resolved;
	}
);

/** The apps that get a tile, in snapshot order. */
export const gridApps = memo<App[], RootState>(
	// The dependency is the resolved list, not the two slices it comes from: a
	// command that touches neither app nor patch leaves the resolved slice by
	// identity, and the grid keeps its cached array instead of re-rendering.
	(state) => [resolvedApps(state)],
	(state) => {
		const apps = resolvedApps(state);
		return apps.order.map((id) => apps.byId[id]).filter((app): app is App => !!app && isGridApp(app));
	}
);

/** Every installed app, including system and headless ones. */
export const allApps = memo<App[], RootState>(
	(state) => [resolvedApps(state)],
	(state) => {
		const apps = resolvedApps(state);
		return apps.order.map((id) => apps.byId[id]).filter((app): app is App => !!app);
	}
);

/** One installed app, with the pending overlays applied. */
export function installedApp(state: RootState, catalogId: string): App | undefined {
	return resolvedApps(state).byId[catalogId];
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

export const appView = memoBy<string, AppView, RootState>(
	(state, catalogId) => {
		// One app's record, not the whole slice: another app's update must leave
		// this tile's view by identity, or every tile re-renders on every change.
		const resolved = resolvedApps(state);
		return [
			resolved.byId[catalogId],
			state.progress.byId[catalogId],
			state.commands.byKey[commandKey('install', catalogId)],
			state.commands.byKey[commandKey('uninstall', catalogId)]
		];
	},
	(state, catalogId) => {
		const app = resolvedApps(state).byId[catalogId];
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
	(state) => [resolvedApps(state), state.launchers, state.layout],
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
 * One writable setting, as a component reads it.
 *
 * `value` is what to show: the pending claim, or the confirmed value when the
 * claim is already true. `confirmed` is the server's answer, which is what a
 * settle predicate compares against. `pending` is true only while a write with a
 * claim is in flight, so a background refresh never disables a form.
 */
export interface ResourceView<T> {
	phase: ResourcePhase;
	value: T | null;
	confirmed: T | null;
	error: string;
	pending: boolean;
}

function viewOf<T>(resource: Resource<T>, value: T | null, pending: boolean): ResourceView<T> {
	return {
		phase: resource.phase,
		value,
		confirmed: resource.value,
		error: resource.error,
		pending
	};
}

export const publicUrlView = memo<ResourceView<string>, RootState>(
	(state) => [state.settings.publicUrl, state.commands],
	(state) => {
		const patch = pendingOverlay(state.commands, 'settings.publicUrl', INSTANCE_KEY);
		const value = patch
			? project('settings.publicUrl', patch.value, state.settings.publicUrl.value)
			: state.settings.publicUrl.value;
		return viewOf(state.settings.publicUrl, value, !!patch);
	}
);

export const timezoneView = memo<ResourceView<string>, RootState>(
	(state) => [state.settings.timezone, state.commands],
	(state) => {
		const patch = pendingOverlay(state.commands, 'settings.timezone', INSTANCE_KEY);
		const value = patch
			? project('settings.timezone', patch.value, state.settings.timezone.value)
			: state.settings.timezone.value;
		return viewOf(state.settings.timezone, value, !!patch);
	}
);

export const aiView = memo<ResourceView<AISettings>, RootState>(
	(state) => [state.settings.ai, state.commands],
	(state) => {
		const patch = pendingOverlay(state.commands, 'settings.ai', INSTANCE_KEY);
		const value = patch
			? project('settings.ai', patch.value, state.settings.ai.value)
			: state.settings.ai.value;
		return viewOf(state.settings.ai, value, !!patch);
	}
);

export const endpointView = memoBy<EndpointContract, ResourceView<EndpointSettings>, RootState>(
	(state, contract) => [state.settings.endpoints[contract], state.commands],
	(state, contract) => {
		const confirmed = state.settings.endpoints[contract];
		const patch = pendingOverlay(state.commands, 'settings.endpoint', contract);
		const value = patch ? project('settings.endpoint', patch.value, confirmed.value) : confirmed.value;
		return viewOf(confirmed, value, !!patch);
	}
);

export const usersView = memo<ResourceView<ManagedUser[]>, RootState>(
	(state) => [state.settings.users, state.commands],
	(state) => {
		const patch = pendingOverlay(state.commands, 'settings.users', INSTANCE_KEY);
		const value = patch
			? project('settings.users', patch.value, state.settings.users.value)
			: state.settings.users.value;
		return viewOf(state.settings.users, value, !!patch);
	}
);

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
