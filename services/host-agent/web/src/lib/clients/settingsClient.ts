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

/**
 * The sign-in block of a provider whose credential is traded for a login rather
 * than pasted.
 *
 * When this is present, the contract's own secret fields are absent from
 * `contracts`: the operator supplies an account, Bloud signs in to that instance,
 * and whatever the remote app hands back is what gets stored. So a remote Seerr
 * asks for a username and password, the credential a person actually has, instead
 * of an API key they would have to go and find.
 */
export interface ExternalProviderExchange {
	/** The contract whose secret fields these replace. */
	contract: string;
	inputs: ExternalProviderField[];
	/**
	 * A login Bloud already holds, described in words. Present means the form can
	 * offer to sign in with that instead of asking, so a password nobody chose
	 * never has to be typed or shown.
	 */
	storedLogin?: string;
}

export interface ExternalProviderOption {
	app: string;
	displayName: string;
	description: string;
	installed: boolean;
	contracts: ExternalProviderContract[];
	exchange?: ExternalProviderExchange;
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
	exchange?: Record<string, string>;
	exchangeLogin?: boolean;
}): Promise<IntentResponse> {
	return post<IntentResponse>('/api/external-apps', {
		kind: 'provider',
		source: `app:${input.app}`,
		name: input.name,
		url: input.url,
		icon: input.icon ?? '',
		values: input.values,
		secrets: input.secrets,
		...(input.exchange ? { exchange: input.exchange } : {}),
		...(input.exchangeLogin ? { exchangeLogin: true } : {})
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
	/**
	 * A fresh sign-in, with the same rule as `secrets`: omitted means "leave the
	 * credential on file", so renaming a remote app never has to sign in again.
	 */
	exchange?: Record<string, string>;
	/** Sign in with the login Bloud holds, rather than with typed inputs. */
	exchangeLogin?: boolean;
}

export function updateExternalApp(id: string, patch: ExternalAppUpdate): Promise<IntentResponse> {
	return patchRequest<IntentResponse>(`/api/external-apps/${id}`, patch);
}
