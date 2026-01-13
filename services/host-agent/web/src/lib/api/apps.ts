/**
 * Apps API - Raw HTTP calls for app management
 * No store coupling - pure functions that return data or throw errors
 */

import type { App, InstallResult, UninstallResult } from '$lib/types';

/**
 * Fetch all installed apps from the server
 */
export async function fetchInstalledApps(): Promise<App[]> {
	const res = await fetch('/api/apps/installed');
	if (!res.ok) {
		const data = await res.json().catch(() => ({}));
		throw new Error(data.error || 'Failed to fetch apps');
	}
	return res.json();
}

/**
 * Install an app with optional integration choices
 */
export async function installApp(
	name: string,
	choices: Record<string, string> = {}
): Promise<InstallResult> {
	const res = await fetch(`/api/apps/${name}/install`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ choices })
	});

	const result: InstallResult = await res.json();

	if (!res.ok) {
		throw new Error(result.error || 'Install failed');
	}

	return result;
}

/**
 * Uninstall an app
 */
export async function uninstallApp(name: string): Promise<UninstallResult> {
	const res = await fetch(`/api/apps/${name}/uninstall`, {
		method: 'POST'
	});

	const result: UninstallResult = await res.json();

	if (!res.ok) {
		throw new Error(result.error || 'Uninstall failed');
	}

	return result;
}
