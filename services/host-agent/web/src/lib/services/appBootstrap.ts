/**
 * App Bootstrap Service
 *
 * Orchestrates the bootstrap process for embedded apps:
 * 1. Fetch app metadata from API
 * 2. Configure SW with app's rewrite settings
 * 3. Execute bootstrap configuration (IndexedDB setup, etc.)
 */

import { fetchAppMetadata } from '$lib/api/catalog';
import { executeBootstrap, type AppMetadata } from '$lib/appConfig';
import { setActiveApp } from './bootstrap';

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

		// Derive needsRewrite from stripPrefix (false = supports BASE_URL = no rewrite)
		const needsRewrite = catalogApp.routing?.stripPrefix !== false;

		// Send to SW so it knows whether to rewrite URLs for this app
		await setActiveApp(appName, needsRewrite);

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
