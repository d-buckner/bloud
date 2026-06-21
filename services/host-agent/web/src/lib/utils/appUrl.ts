/**
 * Build a subdomain URL for an app based on the current window location.
 * Automatically works for dev (localhost:8080) and prod (bloud.local).
 *
 * @param appName - The app name (e.g., "jellyfin")
 * @param path - Optional path to append (e.g., "settings")
 * @returns Full URL like "http://jellyfin.localhost:8080/settings"
 */
export function getAppUrl(appName: string, path: string = ''): string {
	const { hostname, port, protocol } = window.location;
	const portSuffix = port ? `:${port}` : '';
	const pathPrefix = path && !path.startsWith('/') ? '/' : '';
	return `${protocol}//${appName}.${hostname}${portSuffix}${pathPrefix}${path}`;
}
