// SPDX-License-Identifier: AGPL-3.0-only
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
	// Wildcard-domain strip: when the instance is reached on a name of the form
	// bloud.<base> (bloud.foo.ts.net, bloud.example.com with wildcard DNS),
	// apps live under <base> directly, so drop the "bloud." prefix. Any other
	// hostname (localhost, bloud.local, a bare custom domain) is used as-is.
	// The >= 4 label guard keeps a two-label base like "ts.net" from being
	// stripped down to nothing.
	const labels = hostname.split('.');
	const baseDomain =
		hostname.startsWith('bloud.') && hostname.endsWith('.ts.net') && labels.length >= 4
			? labels.slice(1).join('.')
			: hostname;
	return `${protocol}//${appName}.${baseDomain}${portSuffix}${pathPrefix}${path}`;
}
