// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Settings operations - read a setting, or change it and wait for the instance
 * to agree.
 *
 * A save here is an intent: the API answers 202 and the orchestrator applies the
 * change on its own queue. So the operation does three things the forms used to
 * do by hand:
 *
 *  - shows the requested value at once, through an overlay, so the field is not
 *    waiting on a container restart;
 *  - replaces that claim with what the server stored, because the parser
 *    canonicalizes an address and trims a zone;
 *  - polls the read until confirmed state matches, with backoff, so "saved" is a
 *    statement about the instance rather than about the socket.
 *
 * The poll window is long on purpose. A time zone change recreates every
 * container, and the components this replaces gave up after 20 and 60 seconds
 * while the change was still queued.
 */

import {
	fetchAISettings,
	fetchEndpointSettings,
	fetchPublicURL,
	fetchTimezone,
	setAISettings,
	setEndpointSettings,
	setPublicURL,
	setTimezone,
	type AISettings,
	type EndpointContract,
	type EndpointSettings,
	type SetAIRequest,
	type SetEndpointRequest,
	type SetEndpointResponse,
	type PublicURLSettings,
	type SetPublicURLResponse,
	type SetTimezoneResponse,
	type TimezoneSettings
} from '$lib/clients/settingsClient';
import type { IntentResponse } from '$lib/types';
import { settingsLoaded, settingsReadFailed, settingsReadStarted } from '../actions';
import type { SettingsField, SettingsLoadedPayload } from '../reducers/settings';
import { dispatch, getState } from '../kernel';
import { INSTANCE_KEY, satisfied } from '../overlay';
import { commandKey } from '../reducers/commands';
import { messageOf, runCommand } from '../runner';
import type { CommandResult } from '../runner';

/** Read a setting and record it as confirmed. A quiet read on failure: no state. */
async function readInto<T>(read: () => Promise<T>, loaded: (value: T) => SettingsLoadedPayload): Promise<T> {
	const value = await read();
	dispatch(settingsLoaded(loaded(value)));
	return value;
}

/** Read a setting for a form: a failure is visible, and the last value is kept. */
async function loadInto<T>(
	field: SettingsField,
	read: () => Promise<T>,
	loaded: (value: T) => SettingsLoadedPayload
): Promise<T> {
	dispatch(settingsReadStarted({ field }));
	try {
		return await readInto(read, loaded);
	} catch (err) {
		dispatch(settingsReadFailed({ field, error: messageOf(err) }));
		throw err;
	}
}

const publicUrlLoaded = (res: { url: string }): SettingsLoadedPayload => ({
	field: 'publicUrl',
	value: res.url,
	at: Date.now()
});

const timezoneLoaded = (res: { zone: string }): SettingsLoadedPayload => ({
	field: 'timezone',
	value: res.zone,
	at: Date.now()
});

const aiLoaded = (res: AISettings): SettingsLoadedPayload => ({
	field: 'ai',
	value: res,
	at: Date.now()
});

/** The read a form calls on mount. A second call inside a save uses `refresh`. */
export function loadPublicURL(): Promise<CommandResult<PublicURLSettings>> {
	return runCommand({
		key: commandKey('read', 'settings:publicUrl'),
		run: () => loadInto('publicUrl', fetchPublicURL, publicUrlLoaded)
	});
}

export function loadTimezone(): Promise<CommandResult<TimezoneSettings>> {
	return runCommand({
		key: commandKey('read', 'settings:timezone'),
		run: () => loadInto('timezone', fetchTimezone, timezoneLoaded)
	});
}

export function loadAISettings(): Promise<CommandResult<AISettings>> {
	return runCommand({
		key: commandKey('read', 'settings:ai'),
		run: () => loadInto('ai', fetchAISettings, aiLoaded)
	});
}

const refreshPublicUrl = (): Promise<void> =>
	readInto(fetchPublicURL, publicUrlLoaded).then(() => undefined);

const refreshTimezone = (): Promise<void> => readInto(fetchTimezone, timezoneLoaded).then(() => undefined);

const refreshAI = (): Promise<void> => readInto(fetchAISettings, aiLoaded).then(() => undefined);

/**
 * Save the public address.
 *
 * The claim starts as the typed string and is replaced by the canonical origin
 * the PUT returned. Without that step the typed string "bloud.example.com" never
 * matches the stored "http://bloud.example.com", and the wait runs to its cap
 * over a save that already landed.
 */
export function savePublicURL(url: string): Promise<CommandResult<SetPublicURLResponse>> {
	const typed = url.trim();
	return runCommand({
		key: commandKey('settings', 'publicUrl'),
		repeat: 'replace',
		optimistic: { kind: 'settings.publicUrl', key: INSTANCE_KEY, value: { url: typed } },
		failure: 'Could not save the address',
		unconfirmed: 'The new address is not live yet',
		run: () => setPublicURL(typed),
		refine: (res) => ({ kind: 'settings.publicUrl', key: INSTANCE_KEY, value: { url: res.url } }),
		read: refreshPublicUrl,
		settled: (confirmed, res) => confirmed.settings.publicUrl.value === res.url
	});
}

/** Save the instance clock. An empty zone clears the setting. */
export function saveTimezone(zone: string): Promise<CommandResult<SetTimezoneResponse>> {
	const trimmed = zone.trim();
	return runCommand({
		key: commandKey('settings', 'timezone'),
		repeat: 'replace',
		optimistic: { kind: 'settings.timezone', key: INSTANCE_KEY, value: { zone: trimmed } },
		failure: 'Could not save the time zone',
		unconfirmed: 'The new time zone is not live yet',
		run: () => setTimezone(trimmed),
		refine: (res) => ({ kind: 'settings.timezone', key: INSTANCE_KEY, value: { zone: res.zone } }),
		read: refreshTimezone,
		settled: (confirmed, res) => confirmed.settings.timezone.value === res.zone
	});
}

/**
 * The value the AI form will show before the server answers.
 *
 * A blank key field means "keep the stored credential", so the claim keeps
 * `hasApiKey`. `servedTo` is the server's own answer about the instances this
 * setting reaches, so the claim carries the last confirmed one.
 */
export function claimAISettings(input: SetAIRequest, confirmed: AISettings | null): AISettings {
	return {
		upstreams: input.upstreams,
		defaultModel: input.defaultModel,
		hasApiKey: input.apiKey === undefined ? (confirmed?.hasApiKey ?? false) : input.apiKey !== '',
		servedTo: confirmed?.servedTo ?? []
	};
}

export function saveAISettings(input: SetAIRequest): Promise<CommandResult<IntentResponse>> {
	const claim = claimAISettings(input, getState().settings.ai.value);
	return runCommand({
		key: commandKey('settings', 'ai'),
		repeat: 'replace',
		optimistic: { kind: 'settings.ai', key: INSTANCE_KEY, value: { settings: claim } },
		failure: 'Could not save the AI settings',
		unconfirmed: 'The AI settings are not live yet',
		run: () => setAISettings(input),
		read: refreshAI,
		settled: (confirmed) => satisfied('settings.ai', { settings: claim }, confirmed.settings.ai.value)
	});
}

export function loadEndpoint(contract: EndpointContract): Promise<CommandResult<EndpointSettings>> {
	return runCommand({
		key: commandKey('read', `settings:${contract}`),
		run: () =>
			loadInto(
				contract,
				() => fetchEndpointSettings(contract),
				(res) => ({ field: contract, value: res, at: Date.now() })
			)
	});
}

/**
 * The endpoint a save will show before the server answers.
 *
 * The response is authoritative for `configured`, the parsed address, and the
 * model, so `saveEndpoint` rebuilds the claim from it. This first shape only has
 * to be plausible for the moment the request is in flight.
 */
/** A blank key field means "keep the stored credential", so the claim keeps it. */
function keyPresence(apiKey: string | undefined, confirmed: EndpointSettings | null): boolean {
	if (apiKey === undefined) return confirmed?.hasApiKey ?? false;
	return apiKey !== '';
}

function claimEndpoint(
	input: SetEndpointRequest,
	confirmed: EndpointSettings | null
): EndpointSettings {
	const baseUrl = input.baseUrl.trim();
	return {
		configured: baseUrl !== '',
		name: input.name ?? confirmed?.name ?? '',
		baseUrl,
		model: input.model ?? confirmed?.model ?? '',
		hasApiKey: keyPresence(input.apiKey, confirmed),
		servedTo: confirmed?.servedTo ?? []
	};
}

/** The claim as the response stated it: the stored fields win over the typed ones. */
function refinedEndpoint(
	res: SetEndpointResponse,
	claim: EndpointSettings,
	sentKey: string | undefined
): EndpointSettings {
	return {
		configured: res.configured,
		name: claim.name,
		baseUrl: res.baseUrl,
		model: res.model,
		hasApiKey: sentKey === undefined ? claim.hasApiKey : sentKey !== '',
		servedTo: claim.servedTo
	};
}

export function saveEndpoint(
	contract: EndpointContract,
	input: SetEndpointRequest
): Promise<CommandResult<SetEndpointResponse>> {
	const claim = claimEndpoint(input, getState().settings.endpoints[contract].value);
	return runCommand({
		key: commandKey('settings', contract),
		repeat: 'replace',
		optimistic: { kind: 'settings.endpoint', key: contract, value: { settings: claim } },
		failure: `Could not save the ${contract} settings`,
		unconfirmed: `The ${contract} endpoint is not live yet`,
		run: () => setEndpointSettings(contract, input),
		refine: (res) => ({
			kind: 'settings.endpoint',
			key: contract,
			value: { settings: refinedEndpoint(res, claim, input.apiKey) }
		}),
		read: () =>
			readInto(
				() => fetchEndpointSettings(contract),
				(res) => ({ field: contract, value: res, at: Date.now() })
			).then(() => undefined),
		settled: (confirmed, res) =>
			satisfied(
				'settings.endpoint',
				{ settings: refinedEndpoint(res, claim, input.apiKey) },
				confirmed.settings.endpoints[contract].value
			)
	});
}

/** Remove the provider: an empty address, and the credential goes with it. */
export function removeEndpoint(contract: EndpointContract): Promise<CommandResult<SetEndpointResponse>> {
	const confirmed = getState().settings.endpoints[contract].value;
	const claim: EndpointSettings = {
		configured: false,
		name: confirmed?.name ?? '',
		baseUrl: '',
		model: '',
		hasApiKey: false,
		servedTo: []
	};
	return runCommand({
		key: commandKey('settings', contract),
		repeat: 'replace',
		optimistic: { kind: 'settings.endpoint', key: contract, value: { settings: claim } },
		failure: `Could not remove the ${contract} endpoint`,
		unconfirmed: `The ${contract} endpoint is still configured`,
		run: () => setEndpointSettings(contract, { baseUrl: '', name: claim.name, model: '' }),
		read: () =>
			readInto(
				() => fetchEndpointSettings(contract),
				(res) => ({ field: contract, value: res, at: Date.now() })
			).then(() => undefined),
		settled: (confirmed) =>
			satisfied('settings.endpoint', { settings: claim }, confirmed.settings.endpoints[contract].value)
	});
}
