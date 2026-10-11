// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

/**
 * The External app modal adds; it does not manage.
 *
 * It used to end with a list of everything already added, each row with a
 * Remove button. That list was a second, worse copy of two places that already
 * exist: launchers and remote installs are tiles on the dashboard, whose
 * right-click menu configures and removes them, and the inference provider is
 * edited in Settings to AI. A duplicate control surface is how a UI ends up
 * with two answers to one question, so the modal no longer has one.
 *
 * Checked against the source rather than a rendered DOM because the suite has
 * no DOM, and every one of these facts is a literal in the template. Same
 * reasoning as `modalContract.test.ts`.
 */

const SRC = new URL('../../..', import.meta.url).pathname;
const source = readFileSync(join(SRC, 'lib/components/ExternalAppModal.svelte'), 'utf8');

describe('ExternalAppModal: what it opens on', () => {
	it('defaults to the remote app tab', () => {
		expect(source).toMatch(/let kind = \$state<'provider' \| 'launcher'>\('provider'\)/);
	});

	it('puts the remote app tab before the launcher tab', () => {
		// The label sits on its own line inside the button, so match the whole
		// button body rather than a substring that only exists when the markup
		// happens to be written on one line.
		const provider = source.search(/>\s*Remote app\s*<\/button>/);
		const launcher = source.search(/>\s*Launcher\s*<\/button>/);
		expect(provider).toBeGreaterThan(-1);
		expect(launcher).toBeGreaterThan(-1);
		expect(provider).toBeLessThan(launcher);
	});
});

describe('ExternalAppModal: what it does not do', () => {
	it('never reads or removes the existing external apps', () => {
		expect(source).not.toMatch(/fetchExternalApps/);
		expect(source).not.toMatch(/removeExternalApp/);
	});

	it('renders no list of what is already added', () => {
		expect(source).not.toMatch(/<ul/);
		expect(source).not.toMatch(/external-row/);
	});
});

describe('ExternalAppModal: the tab contract', () => {
	it('wires each tab to the panel it stands for', () => {
		expect(source).toMatch(/role="tablist"/);
		expect(source).toMatch(/id="ext-tab-provider"/);
		expect(source).toMatch(/id="ext-tab-launcher"/);
		expect(source).toMatch(/id="ext-tab-panel"/);
		expect(source.match(/aria-controls="ext-tab-panel"/g)).toHaveLength(2);
		expect(source).toMatch(/aria-labelledby=\{kind === 'provider'/);
	});
});
