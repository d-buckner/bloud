// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The store's public surface. A component imports from here and nowhere else
 * inside `store/`: the slices, the reducers, the kernel singleton, the runner,
 * and the effects are implementation details. Keeping them private is what stops
 * the next feature from wiring itself to a reducer, or to a raw `getState()`,
 * instead of to a selector and a command.
 *
 * Two kinds of export answer everything a component does. `select` and the views
 * cover reads. The hooks cover a form, and the action hooks cover the writes a
 * component owns. The command module itself is not exported: a component reaches
 * a write through the hook that owns it, so no component can call a command from
 * another domain by accident.
 */

export { select } from './bindings.svelte';
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
export {
	useAI,
	useAppActions,
	useDashboard,
	useEndpoint,
	useExternalAppActions,
	useGridActions,
	usePublicUrl,
	useTimezone,
	useUsers
} from './hooks';
export type {
	AIForm,
	AppActions,
	EndpointForm,
	ExternalAppActions,
	GridActions,
	LauncherInput,
	ProviderInput,
	TimezoneSetting,
	UsersList
} from './hooks';
export { startGraphPoll, stopGraphPoll } from './effects/graphPoll';
export { PHASE_ORDER } from './wire';
export type { AppProgress, ComponentProgress, PhaseHistoryEntry } from './reducers/progress';
export type { AppView, GraphView, HomeSummary, ResourceView } from './selectors';
export type { RootState } from './reducers';
