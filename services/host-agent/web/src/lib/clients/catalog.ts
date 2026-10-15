// SPDX-License-Identifier: AGPL-3.0-only
/**
 * Catalog API - Raw HTTP calls for app catalog
 */

import type { CatalogApp } from '$lib/types';
import { get } from '$lib/clients/httpClient';

/**
 * Fetch all apps from the catalog
 *
 * The response carries a per-app blocker map beside the list, because whether an
 * app can be installed depends on instance state the catalog metadata does not
 * hold. It is folded onto each app here so every consumer of a CatalogApp sees
 * the same answer the install endpoint will give.
 */
export async function fetchCatalog(): Promise<CatalogApp[]> {
	const data = await get<{
		apps: CatalogApp[];
		installBlockers?: Record<string, string[]>;
	}>('/api/apps');
	const blockers = data?.installBlockers ?? {};
	return (data?.apps ?? [])
		.filter((app) => !app.isSystem)
		.map((app) => {
			const reasons = blockers[app.catalogId];
			return reasons && reasons.length > 0 ? { ...app, installBlockers: reasons } : app;
		});
}

/**
 * Fetch metadata for a specific app
 */
export async function fetchAppMetadata(appName: string): Promise<CatalogApp> {
	return get<CatalogApp>(`/api/apps/${appName}/metadata`);
}
