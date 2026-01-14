/**
 * URL utilities for embedded app iframes
 *
 * Pure functions for building and parsing embed URLs.
 */

/**
 * Build the iframe src URL for an embedded app
 *
 * @param appName - The app identifier (e.g., 'miniflux', 'actual-budget')
 * @param path - The path within the app (e.g., 'settings', 'entries/unread')
 * @returns Full embed URL (e.g., '/embed/miniflux/settings')
 */
export function getEmbedUrl(appName: string, path: string = ''): string {
	const basePath = `/embed/${appName}/`;
	if (!path) {
		return basePath;
	}
	// Avoid double slashes if path starts with /
	const cleanPath = path.startsWith('/') ? path.slice(1) : path;
	return `${basePath}${cleanPath}`;
}

/**
 * Extract the app-relative path from an iframe's current location
 *
 * @param iframePath - The iframe's pathname (e.g., '/embed/miniflux/settings')
 * @param appName - The app identifier
 * @returns The relative path (e.g., 'settings'), or null if not matching
 */
export function extractRelativePath(iframePath: string, appName: string): string | null {
	const embedPrefix = `/embed/${appName}/`;
	if (!iframePath.startsWith(embedPrefix)) {
		return null;
	}
	return iframePath.slice(embedPrefix.length);
}

/**
 * Build the browser URL for an app route
 *
 * @param appName - The app identifier
 * @param path - The path within the app
 * @returns Browser URL (e.g., '/apps/miniflux/settings')
 */
export function getAppRouteUrl(appName: string, path: string = ''): string {
	const basePath = `/apps/${appName}/`;
	if (!path) {
		return basePath;
	}
	const cleanPath = path.startsWith('/') ? path.slice(1) : path;
	return `${basePath}${cleanPath}`;
}
