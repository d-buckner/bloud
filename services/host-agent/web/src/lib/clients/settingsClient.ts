import { get, post, del } from './httpClient';

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

export function setTailnet(data: SetTailnetRequest): Promise<TailnetConnection> {
	return post<TailnetConnection>('/api/settings/tailnet', data);
}

export function deleteTailnet(): Promise<void> {
	return del<void>('/api/settings/tailnet');
}
