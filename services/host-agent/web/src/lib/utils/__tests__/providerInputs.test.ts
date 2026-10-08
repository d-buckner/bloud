// SPDX-License-Identifier: AGPL-3.0-only
import { describe, expect, it } from 'vitest';
import {
	buildProviderPayload,
	deriveProviderInputs,
	missingProviderInputs,
	readProviderInput,
	type ProviderInput
} from '../providerInputs';
import type { ExternalProviderContract } from '$lib/clients/settingsClient';

// The shapes below mirror what the backend derives from the real catalog: a
// value the provider declares statically arrives with `default` set and
// `required` cleared, an optional registry value arrives `required: false`,
// and a secret always arrives `required: true`.
const servarr: ExternalProviderContract[] = [
	{
		name: 'icsFeed',
		fields: [
			{ key: 'calendarName', label: 'Calendar Name', kind: 'value', required: false, default: 'Shows' },
			{
				key: 'path',
				label: 'Path',
				kind: 'value',
				required: false,
				default: '/feed/v3/calendar/Sonarr.ics'
			},
			{ key: 'apiKey', label: 'Api Key', kind: 'secret', required: true }
		]
	},
	{
		name: 'pvr',
		fields: [{ key: 'apiKey', label: 'Api Key', kind: 'secret', required: true }]
	}
];

const jellyfin: ExternalProviderContract[] = [
	{
		name: 'mediaServer',
		fields: [
			{
				key: 'adminUsername',
				label: 'Admin Username',
				kind: 'value',
				required: false,
				default: 'bloud-bootstrap-admin'
			},
			{ key: 'adminPassword', label: 'Admin Password', kind: 'secret', required: true }
		]
	}
];

const affine: ExternalProviderContract[] = [
	{
		name: 'appApi',
		fields: [
			{ key: 'username', label: 'Username', kind: 'value', required: true },
			{ key: 'workspaceId', label: 'Workspace Id', kind: 'value', required: false },
			{
				key: 'password',
				label: 'Password',
				kind: 'secret',
				required: true,
				help: 'A local install mints this itself; for a remote one, read it from that instance.'
			}
		]
	}
];

describe('deriveProviderInputs', () => {
	// The whole point of the change: pointing Bloud at someone's Radarr is two
	// facts, not a form that recites the contract registry.
	it('leaves a Servarr with one input, the API key', () => {
		const inputs = deriveProviderInputs(servarr);
		expect(inputs).toHaveLength(1);
		expect(inputs[0].kind).toBe('secret');
		expect(inputs[0].key).toBe('apiKey');
	});

	it('folds one credential across every contract that declares it', () => {
		expect(deriveProviderInputs(servarr)[0].contracts).toEqual(['icsFeed', 'pvr']);
	});

	it('drops the values the provider already declares statically', () => {
		const keys = deriveProviderInputs(jellyfin).map((input) => input.key);
		expect(keys).not.toContain('adminUsername');
		expect(keys).toEqual(['adminPassword']);
	});

	it('keeps a required value the operator has to supply', () => {
		const inputs = deriveProviderInputs(affine);
		expect(inputs.map((input) => input.key)).toEqual(['username', 'password']);
	});

	it('drops an optional value even when nothing declares it', () => {
		const keys = deriveProviderInputs(affine).map((input) => input.key);
		expect(keys).not.toContain('workspaceId');
	});

	it('carries the help text of the field it folds in', () => {
		expect(deriveProviderInputs(affine)[1].help).toContain('read it from that instance');
	});

	it('survives a provider with no contracts at all', () => {
		expect(deriveProviderInputs([])).toEqual([]);
	});
});

describe('readProviderInput', () => {
	const username: ProviderInput = {
		id: 'value:username',
		key: 'username',
		label: 'Username',
		kind: 'value',
		contracts: ['appApi'],
		help: ''
	};

	it('finds the stored value in any contract the input covers', () => {
		expect(readProviderInput(username, { appApi: { username: 'op@example.com' } })).toBe('op@example.com');
	});

	it('reads blank when the record has nothing for that key', () => {
		expect(readProviderInput(username, { appApi: {} })).toBe('');
	});

	// The list endpoint never echoes a credential, so there is nothing to show.
	it('never reads back a secret', () => {
		const secret: ProviderInput = { ...username, id: 'secret:password', kind: 'secret' };
		expect(readProviderInput(secret, { appApi: { password: 'nope' } })).toBe('');
	});
});

describe('buildProviderPayload', () => {
	it('fans one typed credential out to every contract it covers', () => {
		const inputs = deriveProviderInputs(servarr);
		expect(buildProviderPayload(inputs, { 'secret:apiKey': 'key-123' }).secrets).toEqual({
			icsFeed: 'key-123',
			pvr: 'key-123'
		});
	});

	it('writes typed values under their own contract', () => {
		const payload = buildProviderPayload(deriveProviderInputs(affine), {
			'value:username': 'op@example.com',
			'secret:password': 'pw'
		});
		expect(payload.values).toEqual({ appApi: { username: 'op@example.com' } });
		expect(payload.secrets).toEqual({ appApi: 'pw' });
	});

	// A blank must not become an empty string on the wire: for a credential the
	// server reads that as a typed value and rejects it.
	it('writes nothing for a blank input', () => {
		const payload = buildProviderPayload(deriveProviderInputs(servarr), { 'secret:apiKey': '   ' });
		expect(payload.secrets).toEqual({});
		expect(payload.values).toEqual({});
	});
});

describe('missingProviderInputs', () => {
	it('names the input that is still blank', () => {
		expect(missingProviderInputs(deriveProviderInputs(servarr), {})).toEqual(['Api Key']);
	});

	it('passes once the input is typed', () => {
		expect(missingProviderInputs(deriveProviderInputs(servarr), { 'secret:apiKey': 'k' })).toEqual([]);
	});

	// The edit case: the form cannot show the credential it is only being asked
	// to confirm, so one already on file satisfies the requirement.
	it('treats a stored secret on any covered contract as filled', () => {
		expect(missingProviderInputs(deriveProviderInputs(servarr), {}, ['pvr'])).toEqual([]);
	});

	it('still reports a secret stored for a contract this input does not cover', () => {
		const missing = missingProviderInputs(deriveProviderInputs(affine), { 'value:username': 'op' }, [
			'mediaServer'
		]);
		expect(missing).toEqual(['Password']);
	});

	it('reports every missing input, in form order', () => {
		expect(missingProviderInputs(deriveProviderInputs(affine), {})).toEqual(['Username', 'Password']);
	});
});
