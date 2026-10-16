// SPDX-License-Identifier: AGPL-3.0-only
import { get, post, put, patch as patchRequest, del } from './httpClient';
import type { IntentResponse } from '$lib/types';

export interface PublicURLSettings {
	/** The configured address, as a bare origin: https://bloud.example.com:8443 */
	url: string;
}

export function fetchPublicURL(): Promise<PublicURLSettings> {
	return get<PublicURLSettings>('/api/settings/public-url');
}

export interface SetPublicURLResponse {
	intentId: string;
	/**
	 * The canonical origin the value was stored as. The parser fills in a
	 * missing scheme and drops a redundant one, so this is not necessarily
	 * what was typed, and it is the value the live address converges to.
	 */
	url: string;
}

export function setPublicURL(url: string): Promise<SetPublicURLResponse> {
	return put<SetPublicURLResponse>('/api/settings/public-url', { url });
}

export interface TimezoneSettings {
	/**
	 * The IANA zone name in force, for example Europe/Berlin.
	 * Empty means Bloud states no zone and every container keeps the default its
	 * image ships with, which is UTC in practice.
	 */
	zone: string;
}

export function fetchTimezone(): Promise<TimezoneSettings> {
	return get<TimezoneSettings>('/api/settings/timezone');
}

export interface SetTimezoneResponse {
	intentId: string;
	/**
	 * The zone the save accepted, trimmed. Empty means the setting was cleared.
	 * The UI polls the live value against this string, so it does not have to
	 * reimplement the server's validation.
	 */
	zone: string;
}

/**
 * Set the instance clock. An empty zone clears the setting.
 *
 * The save is an intent: the API answers 202 and the orchestrator recreates the
 * managed containers so they pick the new clock up.
 */
export function setTimezone(zone: string): Promise<SetTimezoneResponse> {
	return put<SetTimezoneResponse>('/api/settings/timezone', { zone });
}

export interface AIUpstream {
	id: string;
	name: string;
	baseUrl: string;
	models?: string[];
	enabled: boolean;
}

export interface AIServedTo {
	app: string;
	via: 'gateway' | 'direct' | 'none';
	model?: string;
}

export interface AISettings {
	upstreams: AIUpstream[];
	defaultModel: string;
	hasApiKey: boolean;
	servedTo: AIServedTo[];
}

export interface SetAIRequest {
	upstreams: AIUpstream[];
	defaultModel: string;
	/**
	 * Omit to keep the stored credential. Send an empty string to clear it.
	 * The API never returns the key, only `hasApiKey`.
	 */
	apiKey?: string;
}

export interface TestAIResponse {
	ok: boolean;
	models?: string[];
	error?: string;
}

export function fetchAISettings(): Promise<AISettings> {
	return get<AISettings>('/api/settings/ai');
}

export function setAISettings(data: SetAIRequest): Promise<IntentResponse> {
	return put<IntentResponse>('/api/settings/ai', data);
}

/**
 * Probe a candidate endpoint's /models. This is an operator-initiated check
 * from the settings form, not a reconciler probe: nothing is pruned from its
 * result, and convergence never consults it.
 */
export function testAIEndpoint(baseUrl: string, apiKey?: string): Promise<TestAIResponse> {
	return post<TestAIResponse>('/api/settings/ai/test', { baseUrl, apiKey });
}

/**
 * One single-endpoint AI capability: `transcription` or `embeddings`.
 *
 * Both are one OpenAI-compatible endpoint the operator declares, one bearer, and
 * one optional model id, and the API serves them from the same module with the
 * same payload. They stay separate contracts because a server that answers one
 * route is not known to answer the other, so the operator states each.
 */
export type EndpointContract = 'transcription' | 'embeddings';

/**
 * The instance's provider for one capability, plus the apps that consume it.
 *
 * It is a separate surface from `AISettings` because it is a separate contract.
 * A server that answers completions is not known to answer audio uploads or
 * return vectors, so the two are configured independently even when one box
 * serves both.
 */
export interface EndpointSettings {
	configured: boolean;
	name: string;
	baseUrl: string;
	model: string;
	hasApiKey: boolean;
	servedTo: string[];
}

export interface SetEndpointRequest {
	/** Empty removes the provider. */
	baseUrl: string;
	name?: string;
	/** Empty leaves the endpoint's own default model in force. */
	model?: string;
	/**
	 * Omit to keep the stored credential. Send an empty string to clear it.
	 * The API never returns the key, only `hasApiKey`.
	 */
	apiKey?: string;
}

/**
 * What the save accepted, as the API canonicalized it.
 *
 * The endpoint is echoed back parsed rather than verbatim, so the form can poll
 * for the stored value without reimplementing the parser, same as the address
 * setting's response.
 */
export interface SetEndpointResponse {
	status: string;
	baseUrl: string;
	model: string;
	configured: boolean;
}

export function fetchEndpointSettings(contract: EndpointContract): Promise<EndpointSettings> {
	return get<EndpointSettings>(`/api/settings/${contract}`);
}

export function setEndpointSettings(
	contract: EndpointContract,
	data: SetEndpointRequest
): Promise<SetEndpointResponse> {
	return put<SetEndpointResponse>(`/api/settings/${contract}`, data);
}

/**
 * List the models a candidate endpoint offers, so the picker shows that server's
 * real models rather than a list Bloud maintains.
 */
export function discoverEndpointModels(
	contract: EndpointContract,
	baseUrl: string,
	apiKey?: string
): Promise<TestAIResponse> {
	return post<TestAIResponse>(`/api/settings/${contract}/models`, { baseUrl, apiKey });
}

// External apps. A launcher is a tile that opens a URL; a provider stands in
// for something Bloud does not run and wires into the integration graph.
export interface ExternalApp {
	id: string;
	kind: 'launcher' | 'provider';
	source: string;
	app?: string;
	name: string;
	url: string;
	icon: string;
	values?: Record<string, Record<string, string>>;
	secretContracts?: string[];
}

export interface ExternalProviderField {
	key: string;
	label: string;
	kind: 'value' | 'secret';
	required: boolean;
	/** Value the provider's catalog entry already declares; prefilled. */
	default?: string;
	help?: string;
}

export interface ExternalProviderContract {
	name: string;
	fields: ExternalProviderField[];
}

export interface ExternalProviderOption {
	app: string;
	displayName: string;
	description: string;
	installed: boolean;
	contracts: ExternalProviderContract[];
}

export function fetchExternalApps(): Promise<ExternalApp[]> {
	return get<ExternalApp[]>('/api/external-apps');
}

export function fetchExternalProviders(): Promise<ExternalProviderOption[]> {
	return get<ExternalProviderOption[]>('/api/external-apps/providers');
}

export function addExternalApp(input: {
	name: string;
	url: string;
	icon: string;
}): Promise<IntentResponse> {
	return post<IntentResponse>('/api/external-apps', { kind: 'launcher', ...input });
}

export function addExternalProvider(input: {
	app: string;
	name: string;
	url: string;
	icon?: string;
	values: Record<string, Record<string, string>>;
	secrets: Record<string, string>;
}): Promise<IntentResponse> {
	return post<IntentResponse>('/api/external-apps', {
		kind: 'provider',
		source: `app:${input.app}`,
		name: input.name,
		url: input.url,
		icon: input.icon ?? '',
		values: input.values,
		secrets: input.secrets,
	});
}

export function removeExternalApp(id: string): Promise<IntentResponse> {
	return del<IntentResponse>(`/api/external-apps/${id}`);
}

/**
 * One field group a PATCH may carry for an existing external app. Every key is
 * optional: the server merges the patch onto the stored record, so a field left
 * out keeps what it has rather than being blanked.
 *
 * `secrets` follows the same rule with sharper consequences. Omitting a
 * contract's credential means "keep the one on file", not "this provider has
 * none", because the list response never echoes a secret back and a form that
 * had to resend what it cannot read could only ever wipe it.
 */
export interface ExternalAppUpdate {
	name?: string;
	url?: string;
	icon?: string;
	values?: Record<string, Record<string, string>>;
	secrets?: Record<string, string>;
}

export function updateExternalApp(id: string, patch: ExternalAppUpdate): Promise<IntentResponse> {
	return patchRequest<IntentResponse>(`/api/external-apps/${id}`, patch);
}
