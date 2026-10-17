// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The field engine: the rules a form gets for free.
 *
 * Three of these cases are the ones a component used to get wrong on its own:
 * `loading` is not "a read is running", `error` is not "a save failed", and
 * `save` clears the edit so the pending claim is what shows. The consent gate is
 * the fourth: consent is a fact about a string, not a flag to reset.
 */

import { describe, expect, it } from 'vitest';
import { guarded, setting } from '../field.svelte';
import type { ResourceView } from '../selectors';

function view<T>(value: T | null, overrides: Partial<ResourceView<T>> = {}): ResourceView<T> {
	return {
		phase: value === null ? 'idle' : 'ready',
		value,
		confirmed: value,
		error: '',
		pending: false,
		...overrides
	};
}

function text(value: string | null, overrides: Partial<ResourceView<string>> = {}): ResourceView<string> {
	return view(value, overrides);
}

describe('the read state a form draws', () => {
	it('is loading until there is something to draw', () => {
		const url = setting({ view: () => text(null), live: () => '', save: () => undefined });

		expect(url.loading).toBe(true);
		expect(url.error).toBe('');
	});

	it('is not loading once a value is known, even while a read runs', () => {
		const url = setting({
			view: () => text('saved', { phase: 'loading' }),
			live: () => 'saved',
			save: () => undefined
		});

		expect(url.loading).toBe(false);
	});

	it('reports a failed read, and only a failed read', () => {
		const url = setting({
			view: () => text('saved', { phase: 'error', error: 'network down' }),
			live: () => 'saved',
			save: () => undefined
		});

		expect(url.error).toBe('network down');
		expect(url.loading).toBe(false);
	});
});

describe('the edit', () => {
	it('starts as the live value and is not dirty', () => {
		const url = setting({ view: () => text('saved'), live: () => 'saved', save: () => undefined });

		expect(url.edit).toBe('saved');
		expect(url.dirty).toBe(false);
	});

	it('becomes the edit and dirty when written', () => {
		const url = setting({ view: () => text('saved'), live: () => 'saved', save: () => undefined });

		url.edit = 'typed';

		expect(url.edit).toBe('typed');
		expect(url.dirty).toBe(true);
	});

	it('does not follow the live value while an edit is present', () => {
		let store = text('first');
		const url = setting({ view: () => store, live: () => store.value ?? '', save: () => undefined });

		url.edit = 'mine';
		store = text('second');

		expect(url.edit).toBe('mine');
	});
});

describe('save', () => {
	it('submits the edit and lets go of it, so the claim is what shows', () => {
		const submitted: string[] = [];
		// The view stays at the confirmed value: in the real store the pending claim
		// would move it, and the point here is that the form no longer owns it.
		const url = setting({
			view: () => text('saved'),
			live: () => 'saved',
			save: (value) => submitted.push(value)
		});

		url.edit = 'typed';
		url.save();

		expect(submitted).toEqual(['typed']);
		expect(url.edit).toBe('saved');
		expect(url.dirty).toBe(false);
	});

	it('reset drops the edit without submitting anything', () => {
		const submitted: string[] = [];
		const url = setting({
			view: () => text('saved'),
			live: () => 'saved',
			save: (value) => submitted.push(value)
		});

		url.edit = 'typed';
		url.reset();

		expect(submitted).toEqual([]);
		expect(url.edit).toBe('saved');
	});

	it('passes the resource facts through', () => {
		const url = setting({
			view: () => text('saved', { pending: true }),
			live: () => 'saved',
			save: () => undefined
		});

		expect(url.pending).toBe(true);
		expect(url.confirmed).toBe('saved');
	});
});

describe('the consent gate', () => {
	function guardedUrl() {
		return guarded({ view: () => text('saved'), live: () => 'saved', save: () => undefined });
	}

	it('starts unacknowledged and unblocked, because nothing changed', () => {
		const url = guardedUrl();

		expect(url.acknowledged).toBe(false);
		expect(url.blocked).toBe(false);
	});

	it('blocks Save once the value differs, until the current text is acknowledged', () => {
		const url = guardedUrl();

		url.edit = 'typed';
		expect(url.blocked).toBe(true);

		url.acknowledged = true;
		expect(url.acknowledged).toBe(true);
		expect(url.blocked).toBe(false);
	});

	it('withdraws consent when the text changes after it', () => {
		const url = guardedUrl();

		url.edit = 'first';
		url.acknowledged = true;
		url.edit = 'second';

		expect(url.acknowledged).toBe(false);
		expect(url.blocked).toBe(true);
	});

	it('restores consent when the text returns to the acknowledged one', () => {
		const url = guardedUrl();

		url.edit = 'first';
		url.acknowledged = true;
		url.edit = 'second';
		url.edit = 'first';

		expect(url.acknowledged).toBe(true);
		expect(url.blocked).toBe(false);
	});

	it('ignores surrounding whitespace, because the API trims', () => {
		const url = guardedUrl();

		url.edit = 'typed';
		url.acknowledged = true;
		url.edit = '  typed ';

		expect(url.acknowledged).toBe(true);
	});

	it('unchecking withdraws consent', () => {
		const url = guardedUrl();

		url.edit = 'typed';
		url.acknowledged = true;
		url.acknowledged = false;

		expect(url.blocked).toBe(true);
	});
});
