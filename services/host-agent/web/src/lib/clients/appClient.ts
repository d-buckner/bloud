// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * App Client - HTTP transport layer for app operations
 */

import { get, post, patch } from './httpClient';
import type { App, IntentResponse } from '$lib/types';

export interface RenameResult {
	success: boolean;
	error?: string;
}

/**
 * Fetch all installed apps
 */
export function fetchInstalledApps(): Promise<App[]> {
	return get<App[]>('/api/apps/installed');
}

/**
 * Install an app (returns immediately with an intent ID; reconciler handles the rest)
 */
export function installApp(name: string): Promise<IntentResponse> {
	return post<IntentResponse>(`/api/apps/${name}/install`);
}

/**
 * Uninstall an app (returns immediately with an intent ID; reconciler handles the rest)
 */
export function uninstallApp(name: string): Promise<IntentResponse> {
	return post<IntentResponse>(`/api/apps/${name}/uninstall`);
}

/**
 * Rename an app's display name (returns intent ID; reconciler handles the rest)
 */
export function renameApp(appName: string, newDisplayName: string): Promise<IntentResponse> {
	return patch<IntentResponse>(`/api/apps/${appName}/rename`, { displayName: newDisplayName });
}
