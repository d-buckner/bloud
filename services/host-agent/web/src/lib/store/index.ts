// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The store's public surface. A component imports from here and nowhere else
 * inside `store/`: the slices, the reducers, the kernel singleton, the runner,
 * and the effects are implementation details. Keeping them private is what stops
 * the next feature from wiring itself to a reducer, or to a raw `getState()`,
 * instead of to a selector and a command.
 *
 * Two exports answer everything a component does: `commands` for every write,
 * and `select` over a selector for every read.
 */

export { select } from './bindings.svelte';
export { commands } from './commands';
export type { Commands } from './commands';
export {
	aiView,
	allApps,
	appView,
	enabledWidgetIds,
	endpointView,
	gridApps,
	graphView,
	homeLoading,
	homeSummary,
	installedApp,
	isAdmin,
	opensProgressInsteadOfLaunching,
	publicUrlView,
	timezoneView,
	usersView
} from './selectors';
export { stopTransport } from './effects/connection';
export { startGraphPoll, stopGraphPoll } from './effects/graphPoll';
export { PHASE_ORDER } from './wire';
export type { AppProgress, ComponentProgress, PhaseHistoryEntry } from './reducers/progress';
export type { AppView, GraphView, HomeSummary, ResourceView } from './selectors';
export type { RootState } from './reducers';
