/**
 * Layout Client - HTTP transport layer for layout operations
 */

import { get, put, isUnauthorized } from './httpClient';
import type { GridElement } from '$lib/stores/layout';


export type Layout = { elements: GridElement[] };

/**
 * Fetch layout from backend API
 * Returns null if unauthorized (401) or on error
 */
export async function fetchLayout(): Promise<Layout | null> {
	try {
		return await get<Layout>('/api/user/layout');
	} catch (err) {
		if (isUnauthorized(err)) return null;
		console.error('Failed to fetch layout from API:', err);
		return null;
	}
}

/**
 * Save layout to backend API
 */
export async function saveLayout(elements: GridElement[]): Promise<void> {
	try {
		await put('/api/user/layout', elements);
	} catch (err) {
		if (!isUnauthorized(err)) {
			console.error('Failed to save layout to API:', err);
		}
	}
}
