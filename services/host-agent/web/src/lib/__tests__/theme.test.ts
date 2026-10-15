// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import {
	nextTheme,
	parseStoredTheme,
	readStoredTheme,
	resolveTheme,
	systemPrefersDark,
	themeLabel,
	writeStoredTheme,
	THEME_STORAGE_KEY
} from '../theme';

/**
 * The theme rule, tested where it is decided.
 *
 * There is no DOM in this suite, which is the reason the decision is a pure
 * function in `theme.ts` rather than a line inside a component. The precedence,
 * the storage that throws, and the value a stale build left behind are all
 * testable without one.
 */

const SRC = new URL('../..', import.meta.url).pathname;

/** A `Storage` stand-in: a map, plus the two failure modes that matter. */
function fakeStorage(initial: Record<string, string> = {}) {
	const map = new Map(Object.entries(initial));
	return {
		map,
		getItem: (key: string) => map.get(key) ?? null,
		setItem: (key: string, value: string) => void map.set(key, value)
	};
}

const throwingStorage = {
	getItem(): string | null {
		throw new Error('storage is blocked');
	},
	setItem(): void {
		throw new Error('storage is blocked');
	}
};

describe('resolveTheme', () => {
	it('follows the system when nothing was chosen', () => {
		expect(resolveTheme(null, false)).toBe('light');
		expect(resolveTheme(null, true)).toBe('dark');
	});

	it('lets a manual choice outrank the system in both directions', () => {
		expect(resolveTheme('light', true)).toBe('light');
		expect(resolveTheme('dark', false)).toBe('dark');
	});
});

describe('parseStoredTheme', () => {
	it.each(['light', 'dark'])('accepts %s', (value) => {
		expect(parseStoredTheme(value)).toBe(value);
	});

	// `auto` is the value a build that offered three states would have stored. A
	// word nothing understands has to fall back to the system, not pin the page.
	it.each(['auto', 'LIGHT', '', 'system', null, undefined, 7, { theme: 'dark' }])(
		'treats %s as no choice',
		(value) => {
			expect(parseStoredTheme(value)).toBeNull();
		}
	);
});

describe('nextTheme', () => {
	it('cycles the two palettes', () => {
		expect(nextTheme('light')).toBe('dark');
		expect(nextTheme('dark')).toBe('light');
	});
});

describe('themeLabel', () => {
	it('names the theme the control is about to give you', () => {
		expect(themeLabel('dark')).toBe('Switch to dark theme');
		expect(themeLabel('light')).toBe('Switch to light theme');
	});
});

describe('readStoredTheme', () => {
	it('reads the choice back from storage', () => {
		expect(readStoredTheme(fakeStorage({ [THEME_STORAGE_KEY]: 'dark' }))).toBe('dark');
	});

	it('ignores a value it cannot act on', () => {
		expect(readStoredTheme(fakeStorage({ [THEME_STORAGE_KEY]: 'sepia' }))).toBeNull();
	});

	it('treats a storage that throws as a storage with nothing in it', () => {
		// Private-mode Safari throws on access. A theme preference is not worth a
		// blank page, and a blank page is what an uncaught throw here buys.
		expect(readStoredTheme(throwingStorage)).toBeNull();
		expect(readStoredTheme(null)).toBeNull();
	});
});

describe('writeStoredTheme', () => {
	it('stores the choice under the one key', () => {
		const storage = fakeStorage();
		writeStoredTheme(storage, 'dark');
		expect(storage.map.get(THEME_STORAGE_KEY)).toBe('dark');
	});

	it('says nothing when storage refuses to take it', () => {
		expect(() => writeStoredTheme(throwingStorage, 'dark')).not.toThrow();
	});
});

describe('systemPrefersDark', () => {
	it('asks the matcher the one question it answers', () => {
		const asked: string[] = [];
		const matches = (query: string) => {
			asked.push(query);
			return true;
		};
		expect(systemPrefersDark(matches)).toBe(true);
		expect(asked).toEqual(['(prefers-color-scheme: dark)']);
	});
});

/**
 * The inline script in `app.html` makes the same decision before the first paint,
 * because a stylesheet cannot. Two copies of a rule is one rule fewer than it
 * looks, so this is the seam that keeps them one rule.
 */
describe('the pre-paint script in app.html', () => {
	const html = readFileSync(join(SRC, 'app.html'), 'utf8');
	const script = html.match(/<script>([\s\S]*?)<\/script>/)?.[1] ?? '';

	it('reads the same storage key the module uses', () => {
		expect(script).toContain(`'${THEME_STORAGE_KEY}'`);
	});

	it('accepts only the two themes it knows', () => {
		expect(script).toContain("'light'");
		expect(script).toContain("'dark'");
	});

	it('asks the operating system the same question', () => {
		expect(script).toContain('(prefers-color-scheme: dark)');
	});

	it('writes the attribute the stylesheet selects on', () => {
		expect(script).toContain('data-theme');
		expect(readFileSync(join(SRC, 'lib/styles/variables.css'), 'utf8')).toContain(
			":root[data-theme='dark']"
		);
	});

	it('declares both palettes to the browser before the stylesheet loads', () => {
		expect(html).toMatch(/<meta name="color-scheme" content="light dark"\s*\/>/);
	});
});
