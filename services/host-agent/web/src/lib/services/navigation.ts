/**
 * Navigation Service - Handles app tab navigation
 *
 * This service orchestrates:
 * - Opening apps (updating tab state + navigating)
 * - Closing apps (updating tab state + navigating to next app or home)
 *
 * Business logic lives here, not in components.
 */

import { goto } from '$app/navigation';
import { tabs } from '$lib/stores/tabs';
import { getApp } from '$lib/stores/apps';
import type { App } from '$lib/types';

/**
 * Open an app - updates tab state and navigates to the app
 *
 * @param appOrName - Either an App object or app name string
 */
export function openApp(appOrName: App | string): void {
	const appName = typeof appOrName === 'string' ? appOrName : appOrName.name;

	// If passed an App object directly (e.g., from clicking on home grid),
	// we know it exists. If passed a name, the tabs store will handle it.
	tabs.open(appName);

	goto(`/apps/${appName}`);
}

/**
 * Close an app tab and navigate appropriately
 *
 * @param appName - Name of the app to close
 * @param currentPath - Current browser path (to check if we're on this app's page)
 */
export function closeApp(appName: string, currentPath: string): void {
	const nextActive = tabs.close(appName);

	// If we're on this app's page, navigate away
	if (currentPath.startsWith(`/apps/${appName}`)) {
		if (nextActive) {
			goto(`/apps/${nextActive}`);
		} else {
			goto('/');
		}
	}
}

/**
 * Navigate to a specific app, ensuring it's open in tabs
 * Used when navigating directly via URL
 *
 * @param appName - Name of the app to navigate to
 */
export function navigateToApp(appName: string): void {
	const app = getApp(appName);
	if (app) {
		tabs.open(appName);
	}
}
