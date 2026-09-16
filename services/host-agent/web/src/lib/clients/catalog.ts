// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
/**
 * Catalog API - Raw HTTP calls for app catalog
 */

import type { CatalogApp } from '$lib/types';
import { get } from '$lib/clients/httpClient';

/**
 * Fetch all apps from the catalog
 */
export async function fetchCatalog(): Promise<CatalogApp[]> {
	const data = await get<{ apps: CatalogApp[] }>('/api/apps');
	return (data?.apps ?? []).filter(app => !app.isSystem);
}

/**
 * Fetch metadata for a specific app
 */
export async function fetchAppMetadata(appName: string): Promise<CatalogApp> {
	return get<CatalogApp>(`/api/apps/${appName}/metadata`);
}
