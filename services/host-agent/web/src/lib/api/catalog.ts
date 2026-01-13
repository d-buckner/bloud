/**
 * Catalog API - Raw HTTP calls for app catalog
 */

import type { CatalogApp } from '$lib/types';

/**
 * Fetch all apps from the catalog
 */
export async function fetchCatalog(): Promise<CatalogApp[]> {
	const res = await fetch('/api/apps');
	if (!res.ok) {
		const data = await res.json().catch(() => ({}));
		throw new Error(data.error || 'Failed to fetch catalog');
	}
	const data = await res.json();
	return data.apps || [];
}

/**
 * Fetch metadata for a specific app
 */
export async function fetchAppMetadata(appName: string): Promise<CatalogApp> {
	const res = await fetch(`/api/apps/${appName}/metadata`);
	if (!res.ok) {
		const data = await res.json().catch(() => ({}));
		throw new Error(data.error || 'Failed to fetch app metadata');
	}
	return res.json();
}
