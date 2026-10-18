// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The hooks a settings component calls.
 *
 * This is the platform's answer to "where does store reactivity live". A
 * component calls one hook, gets a form shaped like its markup, and computes
 * nothing: `loading`, `dirty`, `error`, the consent gate, the probe and the save
 * are all decided here.
 *
 * One file per resource group keeps each module about one thing. The shared
 * mechanics live in `../field.svelte.ts`, and none of it is exported to
 * components.
 */

export { usePublicUrl, useTimezone } from './instance.svelte';
export type { TimezoneSetting } from './instance.svelte';
export { useAI } from './ai.svelte';
export type { AIForm } from './ai.svelte';
export { useEndpoint } from './endpoint.svelte';
export type { EndpointForm } from './endpoint.svelte';
export { useUsers } from './users.svelte';
export type { UsersList } from './users.svelte';
export { useAppActions } from './apps.svelte';
export type { AppActions } from './apps.svelte';
export { useGridActions } from './grid.svelte';
export type { GridActions } from './grid.svelte';
export { useExternalAppActions } from './external.svelte';
export type { ExternalAppActions, LauncherInput, ProviderInput } from './external.svelte';
export { useDashboard } from './dashboard.svelte';
