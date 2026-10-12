// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The store's public surface. A component imports from here and nowhere else
 * inside `store/`: the slices, the reducers, the kernel singleton, and the
 * effects are implementation details. Keeping them private is what stops the
 * next feature from wiring itself to a reducer, or to a raw `getState()`,
 * instead of to a selector and a command.
 */

export { select } from './bindings.svelte';
export {
	allApps,
	appView,
	enabledWidgetIds,
	gridApps,
	graphView,
	homeLoading,
	homeSummary,
	isAdmin,
	opensProgressInsteadOfLaunching
} from './selectors';
export { bootstrapSession, signOut, startDashboard } from './commands/session';
export { installApp, renameApp, uninstallApp } from './commands/apps';
export { removeWidget, saveLayout, toggleWidget } from './commands/layout';
export { stopTransport } from './effects/connection';
export { startGraphPoll, stopGraphPoll } from './effects/graphPoll';
export { PHASE_ORDER } from './wire';
export type { AppProgress, ComponentProgress, PhaseHistoryEntry } from './reducers/progress';
export type { AppView, GraphView, HomeSummary } from './selectors';
export type { RootState } from './reducers';
