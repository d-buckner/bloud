// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
import { get, post, del, put } from './httpClient';
import type { IntentResponse } from '$lib/types';

export interface Host {
	hostname: string;
	primary: boolean;
	builtin: boolean;
}

export interface SetHostsRequest {
	hosts: string[];
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
