// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The field engine: what a form needs from one writable resource, in one object.
 *
 * This module is the platform half of a settings form. It knows four things a
 * component must not:
 *
 *  - how a resource view becomes `loading` and `error`, because that mapping is
 *    a rule about `ResourcePhase`, not about a form;
 *  - that a value is shown through a draft, so a store update cannot reach a
 *    field being typed;
 *  - that `save` submits the edit and then clears it, so the pending claim is
 *    what the field shows from that moment;
 *  - that an acknowledged setting gates Save on the exact text the operator
 *    agreed to, not on a boolean someone has to reset.
 *
 * The public surface is two shapes. `Field` is an editable value with no
 * knowledge of where it came from, which the compound hooks use for each input
 * of a multi-field form. `Setting` adds the resource facts and the save, for a
 * form that edits one resource. `GuardedSetting` adds the acknowledgement gate,
 * for a save whose blast radius needs explicit consent.
 */

import { draft } from './draft.svelte';
import type { ResourceView } from './selectors';

/**
 * A value the operator can edit.
 *
 * `edit`, not `value`, because this is the form's own text and never the store's.
 * A property that looked like the setting would invite a writer to believe it
 * wrote one. A store write is a command, and a command is a method.
 */
export interface Field<T> {
	edit: T;
	readonly dirty: boolean;
	/** Drop the edit, so the live value shows again. */
	reset(): void;
}

/**
 * How one field gets its live value and how it saves.
 *
 * `live` is a getter and a projection: a compound form points several fields at
 * one resource view and each one takes its own slice.
 */
export interface FieldSource<T> {
	/** The value to edit. */
	live: () => T;
	/** Decide what counts as a change. Defaults to identity. */
	same?: (a: T, b: T) => boolean;
}

export function field<T>(source: FieldSource<T>): Field<T> {
	const local = draft(source.live, source.same);
	return {
		get edit(): T {
			return local.value;
		},
		set edit(next: T) {
			local.set(next);
		},
		get dirty(): boolean {
			return local.dirty;
		},
		reset(): void {
			local.clear();
		}
	};
}

/** One writable resource, as a form consumes it. */
export interface Setting<T> extends Field<T> {
	/** What the server says. Null before the first read. */
	readonly confirmed: T | null;
	/** A write with a claim is in flight. */
	readonly pending: boolean;
	/** Nothing has arrived yet, so the form has no shape to draw. */
	readonly loading: boolean;
	/** A failed read, or empty. A failed save is a toast, not this. */
	readonly error: string;
	/** Submit the edit. The field then shows the claim. */
	save(): void;
}

export interface SettingSource<T> extends FieldSource<T> {
	view: () => ResourceView<T>;
	save: (value: T) => void;
}

/**
 * The phase rules, in one place.
 *
 * `loading` is true only until there is something to draw. A refresh over a
 * known value is not a loading state, because blanking a form on every read is
 * worse than showing a value that is a second old.
 */
export function isLoading<T>(view: ResourceView<T>): boolean {
	return view.phase === 'idle' || (view.phase === 'loading' && view.value === null);
}

/** A read failure, or empty. A failed save is a toast, so it is not this. */
export function readError<T>(view: ResourceView<T>): string {
	return view.phase === 'error' ? view.error : '';
}

export function setting<T>(source: SettingSource<T>): Setting<T> {
	const local = field(source);

	return {
		get edit(): T {
			return local.edit;
		},
		set edit(next: T) {
			local.edit = next;
		},
		get dirty(): boolean {
			return local.dirty;
		},
		get confirmed(): T | null {
			return source.view().confirmed;
		},
		get pending(): boolean {
			return source.view().pending;
		},
		get loading(): boolean {
			return isLoading(source.view());
		},
		get error(): string {
			return readError(source.view());
		},
		save(): void {
			// The edit goes to the command and the field lets go of it. From here the
			// claim is what shows, so the typed text stays, the stored value replaces
			// it, and a rejected save falls back to the confirmed one.
			source.save(local.edit);
			local.reset();
		},
		reset(): void {
			local.reset();
		}
	};
}

/** A setting whose save restarts things, so it needs explicit consent. */
export interface GuardedSetting extends Setting<string> {
	acknowledged: boolean;
	/** Save may not run yet: the value changed and the consent does not cover it. */
	readonly blocked: boolean;
}

/**
 * Consent is a fact about a string, not a flag to reset.
 *
 * The operator agreed to restart the apps on *this* address. Editing the field
 * withdraws the consent, because the acknowledged text and the current text stop
 * matching. There is no effect to forget and no reset to wire.
 */
export function guarded(source: SettingSource<string>): GuardedSetting {
	const base = setting(source);
	let acknowledgedFor = $state<string | null>(null);
	const consented = (): boolean => acknowledgedFor !== null && acknowledgedFor === base.edit.trim();

	return {
		get edit(): string {
			return base.edit;
		},
		set edit(next: string) {
			base.edit = next;
		},
		get dirty(): boolean {
			return base.dirty;
		},
		get confirmed(): string | null {
			return base.confirmed;
		},
		get pending(): boolean {
			return base.pending;
		},
		get loading(): boolean {
			return base.loading;
		},
		get error(): string {
			return base.error;
		},
		get acknowledged(): boolean {
			return consented();
		},
		set acknowledged(on: boolean) {
			acknowledgedFor = on ? base.edit.trim() : null;
		},
		get blocked(): boolean {
			return base.dirty && !consented();
		},
		save(): void {
			base.save();
		},
		reset(): void {
			base.reset();
		}
	};
}
