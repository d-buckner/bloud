// SPDX-License-Identifier: AGPL-3.0-only

/**
 * The lifecycle a catalog card renders for one app, collapsed to the four
 * things the card can actually show. `stopped` counts as available: the card
 * offers the same action for it as for an app that was never installed.
 */
export type CatalogCardState = 'available' | 'installed' | 'installing' | 'uninstalling';

export function catalogCardState(status?: string | null): CatalogCardState {
	switch (status) {
		case 'running':
		case 'error':
		case 'failed':
			return 'installed';
		case 'installing':
		case 'starting':
			return 'installing';
		case 'uninstalling':
			return 'uninstalling';
		default:
			return 'available';
	}
}

/**
 * The accessible name of a catalog card.
 *
 * The card's visible body is a paragraph: name, size estimate, category, and a
 * two-line description. Left to the default, the accessible name is that
 * paragraph, and twenty of them in a row is unusable with a screen reader. So
 * the name is written down instead: the action the card performs, then the app.
 * The description stays on screen and out of the name.
 *
 * The name has to contain the card's visible *label*, which is why the control
 * is the title and not the whole card: WCAG 2.5.3 fails a control whose visible
 * text is not inside its accessible name.
 */
export function catalogCardLabel(displayName: string, state: CatalogCardState): string {
	switch (state) {
		case 'installed':
			return `Manage ${displayName}`;
		case 'installing':
			return `Installing ${displayName}`;
		case 'uninstalling':
			return `Uninstalling ${displayName}`;
		default:
			return `View and install ${displayName}`;
	}
}
