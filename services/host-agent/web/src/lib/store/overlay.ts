// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The optimistic overlay: what the user asked for, shown before reality agrees.
 *
 * The backend has one writer (`AGENTS.md`, invariant 1). A settings save is an
 * intent, the orchestrator applies it on its own queue, and that takes seconds or
 * minutes. So the dashboard shows the requested value at once and keeps it until
 * confirmed state catches up. Nothing confirmed is ever overwritten: an overlay
 * is a separate value, and a selector projects it over the confirmed one. A
 * failed command drops the patch, and the old value reappears because it was
 * never gone.
 *
 * Three functions are the whole module, and each one has one job:
 *
 *  - `project` returns the value to show. It returns the confirmed value when the
 *    overlay is already satisfied, so a stale overlay costs nothing.
 *  - `satisfied` says whether confirmed state already carries the overlay's
 *    claim. A command settles on this, never on a projection of itself.
 *  - `pendingOverlay` finds the newest pending patch for one target.
 *
 * The settle rule is the part that is easy to get wrong. A command that settles
 * on "the value I show equals the value I want" settles on its own patch in the
 * same tick it starts, and the user sees a flicker. So predicates read the
 * confirmed slice, and the overlay lives only in `selectors.ts`. That split is a
 * data-flow rule, not a comment: `RootState.settings` holds confirmed values
 * only.
 *
 * A patch lives in the command record, so it exists only while that command is
 * pending. The reducer drops it at a terminal status, and a single load of the
 * store can say whether any overlay is left.
 */

import type { App } from '$lib/types';
import type { AISettings, EndpointSettings } from '$lib/clients/settingsClient';
import type { ManagedUser } from '$lib/clients/userClient';
import type { AppsState } from './reducers/apps';
import type { CommandsState } from './reducers/commands';

/**
 * The key of a setting that exists once per instance.
 *
 * The kind already separates the address from the time zone, so one key covers
 * all of them. An app patches by catalog id and an endpoint by contract name,
 * because those genuinely have more than one of each.
 */
export const INSTANCE_KEY = 'instance';

/** Every surface an overlay can patch. A new one needs a value, a target, and a spec. */
export type OverlayKind =
	| 'apps.record'
	| 'apps.patch'
	| 'settings.publicUrl'
	| 'settings.timezone'
	| 'settings.ai'
	| 'settings.endpoint'
	| 'settings.users';

/** What a patch claims. */
export interface OverlayValues {
	/** An app the server accepted, before the snapshot carries it. */
	'apps.record': { record: App };
	/** One app's fields, for a status change or a rename. */
	'apps.patch': { catalogId: string; patch: Partial<App> };
	'settings.publicUrl': { url: string };
	'settings.timezone': { zone: string };
	'settings.ai': { settings: AISettings };
	'settings.endpoint': { settings: EndpointSettings };
	'settings.users': { users: ManagedUser[] };
}

/** What a patch is projected onto. One confirmed value per kind. */
export interface OverlayTargets {
	'apps.record': AppsState;
	'apps.patch': AppsState;
	'settings.publicUrl': string | null;
	'settings.timezone': string | null;
	'settings.ai': AISettings | null;
	'settings.endpoint': EndpointSettings | null;
	'settings.users': ManagedUser[] | null;
}

/** A patch, tagged so a selector can find the one it owns. */
export type OptimisticPatch = {
	[K in OverlayKind]: { kind: K; key: string; value: OverlayValues[K] };
}[OverlayKind];

/** One kind's two rules. `project` calls both, so neither can be forgotten. */
interface OverlaySpec<V, T> {
	/** Apply the claim to confirmed state. */
	apply(current: T, value: V): T;
	/** Does confirmed state already carry the claim? */
	satisfied(current: T, value: V): boolean;
}

/**
 * Structural equality for the two payloads that have no scalar form.
 *
 * `upstreams` and `users` are small and built in one order by the caller, so a
 * JSON comparison is enough. A deep-equality library would be a dependency for
 * two comparisons.
 */
function sameJSON(a: unknown, b: unknown): boolean {
	return JSON.stringify(a) === JSON.stringify(b);
}

/** Fields the AI save controls. `servedTo` is the server's answer, not ours. */
function sameAIClaim(current: AISettings, wanted: AISettings): boolean {
	return (
		current.defaultModel === wanted.defaultModel &&
		current.hasApiKey === wanted.hasApiKey &&
		sameJSON(current.upstreams, wanted.upstreams)
	);
}

/** Fields the endpoint save controls, the same five the API echoes back. */
function sameEndpointClaim(current: EndpointSettings, wanted: EndpointSettings): boolean {
	return (
		current.baseUrl === wanted.baseUrl &&
		current.name === wanted.name &&
		current.model === wanted.model &&
		current.configured === wanted.configured &&
		current.hasApiKey === wanted.hasApiKey
	);
}

/** A user list matches when every member is there with the same role and state. */
function sameUsers(current: ManagedUser[], wanted: ManagedUser[]): boolean {
	if (current.length !== wanted.length) return false;
	return wanted.every((user) => {
		const found = current.find((candidate) => candidate.username === user.username);
		return !!found && found.is_admin === user.is_admin && found.is_active === user.is_active;
	});
}

const SPECS: { [K in OverlayKind]: OverlaySpec<OverlayValues[K], OverlayTargets[K]> } = {
	'apps.record': {
		// Any snapshot that knows the app is newer than a patch made from a 202, so
		// the confirmed record wins. The overlay only fills the gap before it.
		satisfied: (current, value) => current.byId[value.record.catalog_id] !== undefined,
		apply: (current, value) => {
			const id = value.record.catalog_id;
			if (current.byId[id]) return current;
			return { ...current, byId: { ...current.byId, [id]: value.record }, order: [...current.order, id] };
		}
	},
	'apps.patch': {
		// An app that left the snapshot satisfies every claim about it: an
		// uninstall is done when the record is gone, not when its status changed.
		satisfied: (current, value) => {
			const app = current.byId[value.catalogId];
			if (!app) return true;
			return Object.entries(value.patch).every(
				([field, wanted]) => (app as unknown as Record<string, unknown>)[field] === wanted
			);
		},
		apply: (current, value) => {
			const app = current.byId[value.catalogId];
			if (!app) return current;
			return { ...current, byId: { ...current.byId, [value.catalogId]: { ...app, ...value.patch } } };
		}
	},
	'settings.publicUrl': {
		satisfied: (current, value) => current === value.url,
		apply: (_current, value) => value.url
	},
	'settings.timezone': {
		satisfied: (current, value) => current === value.zone,
		apply: (_current, value) => value.zone
	},
	'settings.ai': {
		satisfied: (current, value) => current !== null && sameAIClaim(current, value.settings),
		apply: (_current, value) => value.settings
	},
	'settings.endpoint': {
		satisfied: (current, value) => current !== null && sameEndpointClaim(current, value.settings),
		apply: (_current, value) => value.settings
	},
	'settings.users': {
		satisfied: (current, value) => current !== null && sameUsers(current, value.users),
		apply: (_current, value) => value.users
	}
};

/** The value to show: the claim, unless confirmed state already carries it. */
export function project<K extends OverlayKind>(
	kind: K,
	value: OverlayValues[K],
	current: OverlayTargets[K]
): OverlayTargets[K] {
	const spec = SPECS[kind];
	if (spec.satisfied(current, value)) return current;
	return spec.apply(current, value);
}

/** Does confirmed state already carry this claim? The settle question. */
export function satisfied<K extends OverlayKind>(
	kind: K,
	value: OverlayValues[K],
	current: OverlayTargets[K]
): boolean {
	return SPECS[kind].satisfied(current, value);
}

/**
 * The newest pending patch for one target, or null.
 *
 * A write and the read that confirms it can name the same target, and only the
 * newest matters. Two writes to one target do not overlap: a value-carrying
 * command supersedes the one before it, so the newest pending patch is the only
 * one that can still be unconfirmed.
 */
export function pendingOverlay<K extends OverlayKind>(
	commands: CommandsState,
	kind: K,
	key: string
): Extract<OptimisticPatch, { kind: K }> | null {
	let newest: { at: number; patch: Extract<OptimisticPatch, { kind: K }> } | null = null;
	for (const record of Object.values(commands.byKey)) {
		if (record.status !== 'pending') continue;
		const patch = record.optimistic;
		if (!patch || patch.kind !== kind || patch.key !== key) continue;
		if (newest && newest.at > record.startedAt) continue;
		newest = { at: record.startedAt, patch: patch as Extract<OptimisticPatch, { kind: K }> };
	}
	return newest?.patch ?? null;
}

/**
 * Every pending patch, oldest first.
 *
 * The apps projection needs this order: a record insert must land before a patch
 * on that record, or a rename on a just-installed app finds nothing to rename.
 */
export function pendingPatches(commands: CommandsState): OptimisticPatch[] {
	return Object.values(commands.byKey)
		.filter((record) => record.status === 'pending' && !!record.optimistic)
		.sort((a, b) => a.startedAt - b.startedAt)
		.map((record) => record.optimistic as OptimisticPatch);
}
