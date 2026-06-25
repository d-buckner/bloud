import { get, post, del } from './httpClient';
import type { IntentResponse } from '$lib/types';

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
