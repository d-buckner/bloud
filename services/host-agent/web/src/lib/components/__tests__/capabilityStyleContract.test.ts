// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The style contract for the two AI capability forms.
 *
 * Svelte scopes a component's CSS to that component. A class named in markup
 * with no rule in the same file is not inherited from anywhere: it renders as a
 * browser default. `InferenceCapability` shipped that way. Its Chat form used
 * `form-field`, `hint`, `pill`, `error-message`, and `loading-state`, and styled
 * none of them, so the section drew bare inputs with a `2px inset` border and a
 * `1px 2px` padding two sections below the themed ones. Nothing caught it: the
 * markup was valid, the classes were real, and the palette was correct
 * everywhere the CSS actually existed.
 *
 * So the contract is asserted against the source, the same way the loading and
 * dialog contracts are. Every class a capability component uses must have a rule
 * in its own style block, and the two components must carry the same form
 * vocabulary, because a field class added to one and not the other is the same
 * bug arriving again.
 */

const SRC = new URL('../../..', import.meta.url).pathname;
const read = (path: string) => readFileSync(join(SRC, path), 'utf8');

const FILES = ['lib/components/InferenceCapability.svelte', 'lib/components/EndpointCapability.svelte'];

/** Classes named in markup, from `class="..."` and every `class:foo` branch. */
function classesUsed(source: string): string[] {
	const markup = source
		.replace(/<script[\s\S]*?<\/script>/g, '')
		.replace(/<style[\s\S]*?<\/style>/g, '');
	const used = new Set<string>();
	for (const match of markup.matchAll(/class="([^"{}]+)"/g)) {
		for (const name of match[1].split(/\s+/)) if (name) used.add(name);
	}
	for (const match of markup.matchAll(/class:([A-Za-z][\w-]*)/g)) used.add(match[1]);
	return [...used].sort();
}

/**
 * Classes a rule actually styles.
 *
 * Only the subject of a selector styles an element: in `.form-field label` the
 * rule paints the `label`, not the wrapper. Counting any mention of a name would
 * call `.form-field` styled on the strength of `.form-field label`, which is
 * exactly the hole this test has to close.
 */
function classesStyled(source: string): Set<string> {
	const style = source.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? '';
	const body = style.replace(/\/\*[\s\S]*?\*\//g, '');
	const out = new Set<string>();
	for (const rule of body.matchAll(/([^{}]+)\{/g)) {
		for (const selector of rule[1].split(',')) {
			// The last compound is the subject; anything before it is context.
			const subject = selector.trim().split(/[\s>+~]+/).pop() ?? '';
			for (const m of subject.matchAll(/\.([A-Za-z][\w-]*)/g)) out.add(m[1]);
		}
	}
	return out;
}

describe('every class a capability form uses is styled in that file', () => {
	it.each(FILES)('%s has no class with no rule', (file) => {
		const source = read(file);
		const styled = classesStyled(source);
		const unstyled = classesUsed(source).filter((c) => !styled.has(c));
		// A non-empty list here is a browser-default control on the page.
		expect(unstyled, `${file} styles none of: ${unstyled.join(', ')}`).toEqual([]);
	});
});

describe('the two capability forms share one vocabulary', () => {
	const shared = classesUsed(read(FILES[0])).filter((c) => classesUsed(read(FILES[1])).includes(c));

	it('finds a shared vocabulary worth guarding', () => {
		// Guards against the test going vacuous if a refactor renames everything.
		expect(shared.length).toBeGreaterThan(8);
	});

	it.each(shared)('both define .%s', (name) => {
		for (const file of FILES) {
			expect(classesStyled(read(file)).has(name), `${file} is missing .${name}`).toBe(true);
		}
	});
});

describe('the field styles name tokens, not literals', () => {
	it.each(FILES)('%s themes the inputs from custom properties', (file) => {
		const source = read(file);
		const style = source.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? '';
		const fieldRule = style.match(/\.form-field input,[\s\S]*?\}/)?.[0] ?? '';
		// A hex here is a colour that cannot change with the palette, which is how
		// the graph labels and the edge strokes failed in dark mode. Which token is
		// the component's business; that it is a token is not.
		expect(fieldRule).toMatch(/background:\s*var\(--color-[a-z-]+\)/);
		expect(fieldRule).toMatch(/color:\s*var\(--color-[a-z-]+\)/);
		expect(fieldRule).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
	});
});

/**
 * The section shell is one rule, not three copies.
 *
 * Svelte scopes a component's CSS to that component, so each settings section
 * carried its own copy of the measure and heading rule. Two had it and the third
 * did not, and nothing failed: the section that lost its copy simply rendered
 * wider with an oversized heading, next to two that looked right.
 */
describe('the settings section shell is defined once, globally', () => {
	const SECTIONS = ['AddressSettingsSection', 'AISettingsSection', 'UsersSettingsSection'];

	it('lives in the global sheet', () => {
		const base = read('lib/styles/base.css');
		expect(base).toMatch(/\.section\s*\{[^}]*max-width/);
		expect(base).toMatch(/\.section h2\s*\{/);
	});

	it.each(SECTIONS)('%s carries no private copy', (name) => {
		const source = read(`lib/components/${name}.svelte`);
		expect(source, `${name} restyles .section locally`).not.toMatch(/\.section\s*\{/);
		expect(source, `${name} restyles .section h2 locally`).not.toMatch(/\.section h2\s*\{/);
	});
});
