// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The draft primitive.
 *
 * These cases pin the arithmetic the forms rely on. The interesting ones are not
 * `set` and `clear`: they are the two shapes that used to need a latch, namely a
 * store update that arrives while an edit is present, and an edit that equals
 * the live value without being absent.
 *
 * The live source is a plain function here, because the subject is the rule and
 * not the subscription. That a component re-renders when the selector notifies
 * is the binding's contract, and the browser pass covers it.
 */

import { describe, expect, it } from 'vitest';
import { draft } from '../draft.svelte';

describe('a draft is the edit, or the live value', () => {
	it('shows the live value with no edit', () => {
		const field = draft(() => 'saved');

		expect(field.value).toBe('saved');
		expect(field.dirty).toBe(false);
	});

	it('shows the edit after it is set', () => {
		const field = draft(() => 'saved');

		field.set('typed');
		expect(field.value).toBe('typed');
		expect(field.dirty).toBe(true);
	});

	it('falls back to the live value when the edit is cleared', () => {
		const field = draft(() => 'saved');

		field.set('typed');
		field.clear();

		expect(field.value).toBe('saved');
		expect(field.dirty).toBe(false);
	});
});

describe('a store update cannot reach a field being edited', () => {
	it('keeps the edit when the live value changes', () => {
		let live = 'first';
		const field = draft(() => live);

		field.set('mine');
		live = 'second';

		expect(field.value).toBe('mine');
	});

	it('follows the live value when no edit is present', () => {
		let live = 'first';
		const field = draft(() => live);

		expect(field.value).toBe('first');
		live = 'second';
		expect(field.value).toBe('second');
	});
});

describe('an edit that equals the live value is still an edit', () => {
	it('is not dirty, but it is owned', () => {
		let live = 'same';
		const field = draft(() => live);

		field.set('same');
		expect(field.dirty).toBe(false);

		// Ownership matters: a store update must not rewrite a field the operator
		// has touched, even when the text happens to equal the live value.
		live = 'changed';
		expect(field.value).toBe('same');
		expect(field.dirty).toBe(true);
	});
});

describe('an empty string is a value', () => {
	it('can be set as an edit over a live value', () => {
		// Clearing the time zone is a real edit, not an absent one.
		const field = draft(() => 'Europe/Berlin');

		field.set('');
		expect(field.value).toBe('');
		expect(field.dirty).toBe(true);
	});

	it('can be the live value that an empty edit falls back to', () => {
		const field = draft(() => '');

		field.set('Europe/Berlin');
		field.clear();

		expect(field.value).toBe('');
		expect(field.dirty).toBe(false);
	});
});

describe('a comparator decides what counts as a change', () => {
	const trimmed = (a: string, b: string) => a.trim() === b.trim();

	it('ignores whitespace it was told to ignore, and keeps the typed text', () => {
		const field = draft(() => 'https://bloud.example.com', trimmed);

		field.set('  https://bloud.example.com ');

		expect(field.dirty).toBe(false);
		// The field keeps the typed text, so the caret and the spacing do not jump
		// while the operator types.
		expect(field.value).toBe('  https://bloud.example.com ');
	});

	it('still detects a real change', () => {
		const field = draft(() => 'https://old.example.com', trimmed);

		field.set('https://new.example.com');

		expect(field.dirty).toBe(true);
	});
});

describe('two drafts are independent', () => {
	it('does not share state', () => {
		const first = draft(() => 'a');
		const second = draft(() => 'b');

		first.set('typed');

		expect(first.value).toBe('typed');
		expect(second.value).toBe('b');
	});
});
