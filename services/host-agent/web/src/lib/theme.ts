// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The theme decision, with no DOM in it.
 *
 * The rule is one line: a stored choice wins, and no stored choice means follow
 * `prefers-color-scheme`. Everything else here is the plumbing that keeps that
 * rule honest: a storage that throws, a stored value from an older build, a
 * system preference that changes while the tab is open.
 *
 * It is a plain module rather than a component because the same three decisions
 * are made twice. The inline script in `app.html` makes them before the first
 * paint, so the page never flashes the wrong palette, and this module makes them
 * again for the control that changes them. `theme.test.ts` pins the two together,
 * because two copies of a rule that can drift apart is one rule fewer than it
 * looks.
 */

/** The two palettes the design system carries. */
export type Theme = 'light' | 'dark';

/**
 * Where the manual choice lives. The absence of the key is a value: it means
 * "follow the system", and it is what lets a visitor who never touches the
 * toggle keep following the operating system as it changes through the day.
 */
export const THEME_STORAGE_KEY = 'bloud-theme';

const THEMES: readonly Theme[] = ['light', 'dark'];

/** True only for a value this module can act on. */
export function isTheme(value: unknown): value is Theme {
	return (THEMES as readonly unknown[]).includes(value);
}

/**
 * Read one stored value.
 *
 * Anything that is not exactly `light` or `dark` is treated as absent, including
 * `auto` from a build that no longer exists. A stale value has to fall back to
 * the system rather than pin the page to a word nothing understands.
 */
export function parseStoredTheme(raw: unknown): Theme | null {
	return isTheme(raw) ? raw : null;
}

/** The precedence, stated once: manual choice, then system preference. */
export function resolveTheme(stored: Theme | null, systemPrefersDark: boolean): Theme {
	return stored ?? (systemPrefersDark ? 'dark' : 'light');
}

/** The other one. The toggle is two-state, so the cycle is two long. */
export function nextTheme(current: Theme): Theme {
	return current === 'dark' ? 'light' : 'dark';
}

/** The words a control says when it offers to switch. */
export function themeLabel(target: Theme): string {
	return target === 'dark' ? 'Switch to dark theme' : 'Switch to light theme';
}

/** The part of `Storage` this module needs, so a test can hand in a map. */
export interface ThemeStorage {
	getItem(key: string): string | null;
	setItem(key: string, value: string): void;
}

/**
 * Read the choice out of a storage.
 *
 * `localStorage` throws on access in a private window and when a site is
 * blocked storage, and a theme preference is not worth a blank page. A storage
 * that fails is a storage with nothing in it.
 */
export function readStoredTheme(storage: ThemeStorage | null | undefined): Theme | null {
	if (!storage) return null;
	try {
		return parseStoredTheme(storage.getItem(THEME_STORAGE_KEY));
	} catch {
		return null;
	}
}

/** Write the choice, and let a storage that refuses to take it stay refused. */
export function writeStoredTheme(storage: ThemeStorage | null | undefined, theme: Theme): void {
	if (!storage) return;
	try {
		storage.setItem(THEME_STORAGE_KEY, theme);
	} catch {
		// Nothing to report: the theme still applies for this visit.
	}
}

/** Whether the operating system asked for a dark palette. */
export function systemPrefersDark(matches: (query: string) => boolean): boolean {
	return matches('(prefers-color-scheme: dark)');
}
