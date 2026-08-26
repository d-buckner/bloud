// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
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
	// For Tailscale MagicDNS tailnet domains (bloud.foo.ts.net, always 4
	// labels ending in .ts.net), strip the "bloud." prefix so apps use the
	// tailnet domain (e.g., navidrome.foo.ts.net). Any other hostname —
	// localhost, bloud.local, or a custom domain like bloud.example.com — is
	// used as-is.
	const labels = hostname.split('.');
	const baseDomain =
		hostname.startsWith('bloud.') && hostname.endsWith('.ts.net') && labels.length >= 4
			? labels.slice(1).join('.')
			: hostname;
	return `${protocol}//${appName}.${baseDomain}${portSuffix}${pathPrefix}${path}`;
}

/**
 * Build a subdomain URL for a remote (shared) app.
 * The subdomain is "{appId}-{hostLabel-slug}.{hostname}".
 *
 * @param appId - The app identifier (e.g., "jellyfin")
 * @param hostLabel - The remote host label (e.g., "Johan")
 * @returns Full URL like "http://jellyfin-johan.localhost:8080"
 */
export function getRemoteAppUrl(appId: string, hostLabel: string): string {
	const slug = `${appId}-${slugify(hostLabel)}`;
	const { hostname, port, protocol } = window.location;
	const portSuffix = port ? `:${port}` : '';
	return `${protocol}//${slug}.${hostname}${portSuffix}`;
}

function slugify(s: string): string {
	return s
		.toLowerCase()
		.replace(/[^a-z0-9]+/g, '-')
		.replace(/^-|-$/g, '');
}
