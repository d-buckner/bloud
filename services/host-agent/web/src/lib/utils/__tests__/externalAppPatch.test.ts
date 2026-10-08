// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import { launcherPatch, missingRequiredFields, providerPatch } from '../externalAppPatch';

const emptyForm = {
	name: 'NAS Jellyfin',
	url: 'https://jellyfin.example.com',
	icon: '',
	values: {},
	secrets: {}
};

describe('launcherPatch', () => {
	it('trims the three fields a launcher has', () => {
		expect(
			launcherPatch({ ...emptyForm, name: '  My NAS  ', url: ' https://nas.local ', icon: '  http://i ' })
		).toEqual({ name: 'My NAS', url: 'https://nas.local', icon: 'http://i' });
	});

	it('sends no credential section at all', () => {
		const patch = launcherPatch({ ...emptyForm, secrets: { mediaServer: 'ignored' } });
		expect(patch).not.toHaveProperty('secrets');
	});
});

describe('providerPatch', () => {
	it('carries the endpoint and the contract values', () => {
		const patch = providerPatch({
			...emptyForm,
			values: { mediaServer: { adminUsername: 'daniel' } }
		});
		expect(patch.name).toBe('NAS Jellyfin');
		expect(patch.url).toBe('https://jellyfin.example.com');
		expect(patch.values).toEqual({ mediaServer: { adminUsername: 'daniel' } });
	});

	// The rule the whole edit flow rests on. The list endpoint never returns the
	// credential, so a form that re-sent a blank one would be asserting the
	// operator typed it, and the server rejects that. Dropping the key is what
	// makes "save the name without retyping the password" possible.
	it('omits the secrets key entirely when nothing was typed', () => {
		const patch = providerPatch({
			...emptyForm,
			secrets: { mediaServer: '', appApi: '   ' }
		});
		expect(patch).not.toHaveProperty('secrets');
	});

	it('sends only the credentials the operator typed', () => {
		const patch = providerPatch({
			...emptyForm,
			secrets: { mediaServer: '   ', appApi: 'rotated-secret' }
		});
		expect(patch.secrets).toEqual({ appApi: 'rotated-secret' });
	});

	it('never puts the icon on a provider patch', () => {
		// A remote install borrows the catalog icon for its tile, so the record's
		// own icon field does nothing here and should not be written.
		const patch = providerPatch({ ...emptyForm, icon: 'https://example.com/icon.png' });
		expect(patch).not.toHaveProperty('icon');
	});
});

describe('missingRequiredFields', () => {
	const required = [
		{ contract: 'mediaServer', key: 'adminUsername', label: 'Admin username', kind: 'value' as const },
		{ contract: 'mediaServer', key: 'adminPassword', label: 'Admin password', kind: 'secret' as const }
	];

	it('names the name and the endpoint before anything else', () => {
		expect(
			missingRequiredFields({ ...emptyForm, name: '  ', url: '' }, [], [])
		).toEqual(['name', 'endpoint']);
	});

	it('reports an empty required value', () => {
		const missing = missingRequiredFields({ ...emptyForm, values: {} }, required, []);
		expect(missing).toContain('Admin username');
	});

	it('passes a required value that is filled', () => {
		const form = {
		...emptyForm,
		values: { mediaServer: { adminUsername: 'daniel' } },
		secrets: { mediaServer: 'pw' }
	};
		expect(missingRequiredFields(form, required, [])).toEqual([]);
	});

	// The stored-credential case: the form shows a blank password box because
	// it cannot read the one on file, and the operator should still be able to
	// save a name change without touching it.
	it('treats a stored secret as satisfying its own requirement', () => {
		const form = { ...emptyForm, values: { mediaServer: { adminUsername: 'daniel' } } };
		expect(missingRequiredFields(form, required, ['mediaServer'])).toEqual([]);
	});

	it('still reports a stored secret for a different contract', () => {
		const form = { ...emptyForm, values: { mediaServer: { adminUsername: 'daniel' } } };
		expect(missingRequiredFields(form, required, ['appApi'])).toEqual(['Admin password']);
	});

	it('ignores optional fields', () => {
		const form = { ...emptyForm, secrets: { other: '' } };
		expect(missingRequiredFields(form, [], [])).toEqual([]);
	});
});
