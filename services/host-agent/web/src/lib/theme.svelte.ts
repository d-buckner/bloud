// SPDX-License-Identifier: AGPL-3.0-only
/**
 * The theme, bound to the document and to the control that changes it.
 *
 * The decision itself lives in `theme.ts`, where a test can reach it. This file
 * is only the three things that need a browser: read the stored choice, write
 * `data-theme` on the root element, and keep listening to the operating system.
 *
 * The inline script in `app.html` already put the right value on the document
 * before this module was loaded. Nothing here has to run before the first paint,
 * which is what keeps the palette out of the component tree.
 */

import {
	nextTheme,
	readStoredTheme,
	resolveTheme,
	systemPrefersDark,
	writeStoredTheme,
	type Theme,
	type ThemeStorage
} from './theme';

/** The browser's storage, or nothing where there is no browser. */
function browserStorage(): ThemeStorage | null {
	try {
		return typeof window === 'undefined' ? null : window.localStorage;
	} catch {
		// A blocked or private storage throws on access, not on use.
		return null;
	}
}

function systemIsDarkNow(): boolean {
	if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
	return systemPrefersDark((query) => window.matchMedia(query).matches);
}

/** Null is a real value here: it means the visitor has not chosen. */
let storedChoice = $state<Theme | null>(readStoredTheme(browserStorage()));
let systemIsDark = $state<boolean>(systemIsDarkNow());

/** Puts the palette on the document. The only place that touches `data-theme`. */
export function applyTheme(theme: Theme): void {
	if (typeof document === 'undefined') return;
	document.documentElement.dataset.theme = theme;
}

export const theme = {
	/** What the page is, after the choice and the system are settled. */
	get resolved(): Theme {
		return resolveTheme(storedChoice, systemIsDark);
	},
	/** Records a manual choice, which outranks the system from here on. */
	choose(next: Theme): void {
		storedChoice = next;
		writeStoredTheme(browserStorage(), next);
		applyTheme(next);
	},
	toggle(): void {
		theme.choose(nextTheme(theme.resolved));
	}
};

/**
 * Follow the operating system until the visitor picks a side.
 *
 * The listener stays on after a manual choice, because the choice can be
 * removed and the value it would fall back to has to be current when it is.
 * Returns the teardown; the layout calls it on unmount.
 */
export function watchSystemTheme(): () => void {
	if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return () => {};
	const query = window.matchMedia('(prefers-color-scheme: dark)');
	const onChange = (event: MediaQueryListEvent): void => {
		systemIsDark = event.matches;
	};
	systemIsDark = query.matches;
	query.addEventListener('change', onChange);
	return () => query.removeEventListener('change', onChange);
}
