// SPDX-License-Identifier: AGPL-3.0-only
/**
 * An editable value: a draft that falls back to the live one.
 *
 * A form has two jobs that look like one. It shows a value the store owns, and
 * it holds an edit the operator has not submitted. Keeping the second as a copy
 * of the first is the shape that grows a sync effect, then a latch to stop that
 * effect overwriting a keystroke, then a second latch for the day the copy has
 * to be reset. None of that is the form's problem. An edit is either present or
 * absent, and while it is absent the live value shows.
 *
 * That is the whole module. `set` records an edit, `clear` drops it, and `value`
 * is the edit when there is one and the live value otherwise. There is no
 * effect, no watcher, and no ordering to get right.
 *
 * Two properties follow from that shape, and they are why the latches are gone:
 *
 *  - While an edit is present the live value is not read at all, so a store
 *    update cannot reach the field. Not clobbering a keystroke is arithmetic,
 *    not a rule someone has to remember.
 *  - With no edit, `dirty` never reads the live value either, so a form that is
 *    not being edited does not subscribe to the value through this call.
 *
 * The contract has one rule, and it is why a form never fights the store: **call
 * `clear()` when the command starts, not when it finishes.** From that moment
 * the pending claim is the value to show, so the field keeps the submitted text,
 * adopts the server's canonical form when the claim is refined, and falls back
 * to the confirmed value if the command fails. The form stops owning the value
 * the instant it asks for a change.
 *
 * The typed value lives in a box rather than beside a sentinel. An empty string
 * is a real time zone and a real display name, so `undefined` cannot mean "no
 * draft" without taking a value away from the caller's own type.
 */

/**
 * One editable value.
 *
 * `live` is a getter, not a value. That is what lets `select()` supply it: the
 * read happens when this module asks, which is inside whatever tracked scope is
 * reading `value` or `dirty`.
 */
export interface Draft<T> {
	/** The typed value while an edit is present, the live value otherwise. */
	readonly value: T;
	/** An edit is present and it differs from the live value. */
	readonly dirty: boolean;
	/** Record an edit. */
	set(value: T): void;
	/** Drop the edit, so the live value shows again. Call it on submit. */
	clear(): void;
}

/**
 * Whitespace-only differences are not edits.
 *
 * Every text setting the dashboard edits is trimmed by the API before it is
 * stored, so a trailing space is not a change to submit. The field still shows
 * the typed text, because rewriting it under the caret is worse than a dirty
 * check that ignores it.
 */
export const sameText = (a: string, b: string): boolean => a.trim() === b.trim();

/**
 * Make a value editable against a live source.
 *
 * `same` exists for a draft whose type has no stable identity, because Svelte
 * boxes an object assigned to state and `Object.is` then compares the box. The
 * default is right for every value the dashboard edits today: a string.
 */
export function draft<T>(live: () => T, same: (a: T, b: T) => boolean = Object.is): Draft<T> {
	let typed = $state<{ value: T } | null>(null);

	return {
		get value(): T {
			return typed === null ? live() : typed.value;
		},
		get dirty(): boolean {
			return typed !== null && !same(typed.value, live());
		},
		set(value: T): void {
			typed = { value };
		},
		clear(): void {
			typed = null;
		}
	};
}
