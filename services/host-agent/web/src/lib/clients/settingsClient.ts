// SPDX-License-Identifier: AGPL-3.0-only
import { get, post, del, put } from './httpClient';
import type { IntentResponse } from '$lib/types';

export type HostScheme = 'http' | 'https';

export interface Host {
	hostname: string;
	primary: boolean;
	builtin: boolean;
	/**
	 * The scheme this host is served under. Built-in hosts report their fixed
	 * mapping; an admin-added host carries the scheme stored for it, which is
	 * what makes a TLS-terminating proxy in front of Bloud expressible.
	 */
	scheme: HostScheme;
}

export interface SetHostsRequest {
	hosts: string[];
	/**
	 * Per-host scheme. Omitting a host here leaves whatever is already stored for
	 * it, so a save that never sends the field silently drops an existing https
	 * back to http. The settings UI sends the scheme for every non-builtin host
	 * it is showing.
	 */
	schemes?: Record<string, HostScheme>;
	primary: string;
}

export function fetchHosts(): Promise<{ hosts: Host[] }> {
	return get<{ hosts: Host[] }>('/api/settings/hosts');
}

export function setHosts(data: SetHostsRequest): Promise<IntentResponse> {
	return put<IntentResponse>('/api/settings/hosts', data);
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
