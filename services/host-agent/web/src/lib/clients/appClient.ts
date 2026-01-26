/**
 * App Client - HTTP transport layer for app operations
 */

import { get, post, patch } from './httpClient';
import type { App, InstallResult, UninstallResult } from '$lib/types';

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
 * Install an app with optional integration choices
 */
export function installApp(name: string, choices: Record<string, string> = {}): Promise<InstallResult> {
	return post<InstallResult>(`/api/apps/${name}/install`, { choices });
}

/**
 * Uninstall an app
 */
export function uninstallApp(name: string): Promise<UninstallResult> {
	return post<UninstallResult>(`/api/apps/${name}/uninstall`);
}

/**
 * Rename an app's display name
 */
export async function renameApp(appName: string, newDisplayName: string): Promise<RenameResult> {
	try {
		await patch(`/api/apps/${appName}/rename`, { displayName: newDisplayName });
		return { success: true };
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Rename failed';
		return { success: false, error: message };
	}
}
