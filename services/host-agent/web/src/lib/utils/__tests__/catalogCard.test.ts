// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { catalogCardLabel, catalogCardState } from '../catalogCard';

describe('catalogCardState', () => {
	it('counts a live, degraded, and failed install as installed', () => {
		expect(catalogCardState('running')).toBe('installed');
		expect(catalogCardState('error')).toBe('installed');
		expect(catalogCardState('failed')).toBe('installed');
	});

	it('counts the bring-up phases as installing', () => {
		expect(catalogCardState('installing')).toBe('installing');
		expect(catalogCardState('starting')).toBe('installing');
	});

	it('counts teardown as uninstalling', () => {
		expect(catalogCardState('uninstalling')).toBe('uninstalling');
	});

	it('counts a stopped app and no install at all as available', () => {
		expect(catalogCardState('stopped')).toBe('available');
		expect(catalogCardState(null)).toBe('available');
		expect(catalogCardState(undefined)).toBe('available');
	});
});

describe('catalogCardLabel', () => {
	it('names the action and the app, never the card body', () => {
		expect(catalogCardLabel('Jellyfin', 'available')).toBe('View and install Jellyfin');
		expect(catalogCardLabel('Jellyfin', 'installed')).toBe('Manage Jellyfin');
	});

	it('names the in-flight state while the card cannot be used', () => {
		expect(catalogCardLabel('Jellyfin', 'installing')).toBe('Installing Jellyfin');
		expect(catalogCardLabel('Jellyfin', 'uninstalling')).toBe('Uninstalling Jellyfin');
	});

	it('stays short enough to announce, whatever the app is called', () => {
		// The regression this pins: with no explicit name the accessible name
		// was the whole card, 98 to 158 characters of size, category, and
		// description.
		const label = catalogCardLabel('Paperless-ngx', 'installed');
		expect(label.length).toBeLessThan(30);
		expect(label).not.toMatch(/MB|GB|PRODUCTIVITY|knowledge base/);
	});
});
