/**
 * App Bootstrap Service
 *
 * Orchestrates the bootstrap process for embedded apps:
 * 1. Fetch app metadata from API
 * 2. Execute bootstrap configuration (IndexedDB setup, etc.)
 */

import { fetchAppMetadata } from '$lib/api/catalog';
import { executeBootstrap, type AppMetadata } from '$lib/appConfig';

export interface BootstrapResult {
	success: boolean;
	error?: string;
}

/**
 * Bootstrap an app - fetch metadata and execute bootstrap config
 */
export async function bootstrapApp(appName: string): Promise<BootstrapResult> {
	try {
		const catalogApp = await fetchAppMetadata(appName);

		const appMetadata: AppMetadata = {
			...catalogApp,
			origin: window.location.origin,
			embedUrl: `${window.location.origin}/embed/${appName}`
		};

		const result = await executeBootstrap(appName, catalogApp.bootstrap, appMetadata);

		if (!result.success) {
			return { success: false, error: result.error };
		}

		return { success: true };
	} catch (err) {
		const message = err instanceof Error ? err.message : 'Bootstrap failed';
		return { success: false, error: message };
	}
}
