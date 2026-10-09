// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The dialog contract, checked against the source rather than a rendered DOM.
 *
 * A modal that forgets its semantics is invisible in review: the overlay looks
 * identical, and the missing `aria-labelledby` only shows up when someone
 * using a screen reader has to listen to a whole form to learn what it is
 * called. So the contract is data the build can assert, the same way
 * `TestRegisterAll` asserts that a configurator is registered where it is
 * declared.
 *
 * It runs on source text because the suite has no DOM: adding a browser
 * environment to assert an attribute that is written literally in the template
 * would cost a dependency to re-check a string.
 */

const SRC = new URL('../../..', import.meta.url).pathname;

function svelteFiles(dir: string): string[] {
	return readdirSync(dir).flatMap((entry) => {
		const full = join(dir, entry);
		if (entry === 'node_modules' || entry === '__tests__' || entry === '.svelte-kit') return [];
		if (statSync(full).isDirectory()) return svelteFiles(full);
		return full.endsWith('.svelte') ? [full] : [];
	});
}

const files = svelteFiles(SRC);
const modalCallers = files.filter((f) =>
	/import\s+Modal\s+from\s+['"][^'"]*Modal\.svelte['"]/.test(readFileSync(f, 'utf8'))
);

describe('Modal.svelte', () => {
	const source = readFileSync(join(SRC, 'lib/components/Modal.svelte'), 'utf8');

	it('exposes the dialog semantics every caller depends on', () => {
		expect(source).toMatch(/role=\{dialogRole\}/);
		expect(source).toMatch(/aria-modal="true"/);
		expect(source).toMatch(/aria-labelledby=\{labelledBy\}/);
	});

	it('locks the page scroll and marks the background inert', () => {
		expect(source).toMatch(/style\.overflow = 'hidden'/);
		expect(source).toMatch(/\.inert = true/);
	});

	it('traps Tab and returns focus to the trigger', () => {
		expect(source).toMatch(/function trapTab/);
		expect(source).toMatch(/trigger\.focus\(\)/);
	});
});

describe('every modal caller', () => {
	it('finds the modals to check', () => {
		expect(modalCallers.length).toBeGreaterThanOrEqual(9);
	});

	it.each(modalCallers.map((f) => [f.replace(SRC, 'src/'), readFileSync(f, 'utf8')]))(
		'%s names its dialog',
		(_file, source) => {
			const labels = [...source.matchAll(/labelledBy="([^"]+)"/g)].map((m) => m[1]);
			expect(labels.length, 'every <Modal> must pass a labelledBy id').toBeGreaterThan(0);

			for (const id of labels) {
				expect(source, `labelledBy points at "${id}", which is not in this file`).toContain(
					`id="${id}"`
				);
			}
		}
	);
});
