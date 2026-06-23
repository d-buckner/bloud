/**
 * Remote App Client - HTTP transport for shared app operations
 */

import { get, post, del } from './httpClient';
import type { RemoteApp } from '$lib/types';

interface AddRemoteAppRequest {
	appId: string;
	tailnetAddr: string;
	hostLabel: string;
}

/**
 * Fetch all remote (shared) apps
 */
export function fetchRemoteApps(): Promise<RemoteApp[]> {
	return get<RemoteApp[]>('/api/sharing/remote-apps');
}

/**
 * Add a shared app from a remote host
 */
export function addRemoteApp(data: AddRemoteAppRequest): Promise<RemoteApp> {
	return post<RemoteApp>('/api/sharing/remote-apps', data);
}

/**
 * Remove a remote app
 */
export function removeRemoteApp(id: string): Promise<void> {
	return del<void>(`/api/sharing/remote-apps/${id}`);
}
