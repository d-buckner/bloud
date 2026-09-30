// SPDX-License-Identifier: AGPL-3.0-only
import { get, post, del, put } from './httpClient';
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

export interface TailnetConnection {
	id: string;
	name: string;
	type: string;
	hasAuthKey: boolean;
	controlUrl: string;
	status: string;
}

export interface SetTailnetRequest {
	name: string;
	type: 'tailscale' | 'headscale';
	authKey: string;
	controlUrl?: string;
}

export function fetchTailnet(): Promise<TailnetConnection | null> {
	return get<TailnetConnection | null>('/api/settings/tailnet');
}

export function setTailnet(data: SetTailnetRequest): Promise<IntentResponse> {
	return post<IntentResponse>('/api/settings/tailnet', data);
}

export function deleteTailnet(): Promise<IntentResponse> {
	return del<IntentResponse>('/api/settings/tailnet');
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
