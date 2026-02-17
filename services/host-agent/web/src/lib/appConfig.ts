// Metadata-driven app pre-configuration
// Executes bootstrap config from metadata.yaml before loading app in iframe

import type { BootstrapConfig, CatalogApp, IndexedDBConfig, IndexedDBEntry, LocalStorageConfig } from './types';
import type { InterceptConfig, IndexedDBInterceptConfig, LocalStorageInterceptConfig } from '../service-worker/inject';
import { MessageType } from '../service-worker/types';

export interface BootstrapResult {
	success: boolean;
	error?: string;
}

/**
 * Template variables for bootstrap config substitution.
 * Includes all CatalogApp fields plus computed runtime values.
 */
export interface AppMetadata extends CatalogApp {
	origin: string;
	embedUrl: string;
	[key: string]: unknown; // Allow indexing for template substitution
}

const configuredApps = new Map<string, BootstrapResult>();

/**
 * Execute bootstrap configuration for an app.
 * Reads declarative config from metadata.yaml and performs the required operations.
 */
export async function executeBootstrap(
	appName: string,
	config: BootstrapConfig | undefined,
	metadata: AppMetadata
): Promise<BootstrapResult> {
	// Check if already successfully configured
	const cached = configuredApps.get(appName);
	if (cached?.success) {
		return cached;
	}

	// No bootstrap config means nothing to do
	if (!config) {
		const result = { success: true };
		configuredApps.set(appName, result);
		return result;
	}

	try {
		const hasIndexedDB = config.indexedDB?.intercepts?.length || config.indexedDB?.writes?.length;
		const hasLocalStorage = config.localStorage?.intercepts?.length;

		if (hasIndexedDB || hasLocalStorage) {
			console.log('[appConfig] Bootstrap config:', {
				indexedDB: config.indexedDB ? {
					database: config.indexedDB.database,
					intercepts: config.indexedDB.intercepts?.length ?? 0,
					writes: config.indexedDB.writes?.length ?? 0,
				} : null,
				localStorage: config.localStorage ? {
					intercepts: config.localStorage.intercepts?.length ?? 0,
				} : null,
			});

			// Send intercepts to service worker for injection into iframe
			await sendInterceptsToSW(config, metadata);

			// Write IndexedDB entries from main page (for values apps don't overwrite)
			if (config.indexedDB) {
				await writeIndexedDBEntries(config.indexedDB, metadata);
			}
		}

		const result = { success: true };
		configuredApps.set(appName, result);
		return result;
	} catch (err) {
		const error = err instanceof Error ? err.message : 'Bootstrap failed';
		const result = { success: false, error };
		// Don't cache failures - allow retry
		return result;
	}
}

/**
 * Clear cached bootstrap state for an app (allows retry)
 */
export function clearBootstrapCache(appName: string): void {
	configuredApps.delete(appName);
}

/**
 * Send unified intercept config to service worker.
 * The SW will inject a script into iframe HTML that patches IndexedDB and localStorage.
 */
async function sendInterceptsToSW(config: BootstrapConfig, metadata: AppMetadata): Promise<void> {
	const interceptConfig: InterceptConfig = {};

	// Build IndexedDB intercept config
	if (config.indexedDB?.intercepts?.length) {
		interceptConfig.indexedDB = buildIndexedDBConfig(config.indexedDB, metadata);
	}

	// Build localStorage intercept config
	if (config.localStorage?.intercepts?.length) {
		interceptConfig.localStorage = buildLocalStorageConfig(config.localStorage, metadata);
	}

	// Check if there's anything to send
	const hasIntercepts = interceptConfig.indexedDB || interceptConfig.localStorage;

	if (!hasIntercepts) {
		console.log('[appConfig] No intercepts to send, clearing SW config');
		await postMessageToSW({
			type: MessageType.SET_INTERCEPTS,
			config: null
		});
		return;
	}

	console.log('[appConfig] Sending intercepts to SW:', interceptConfig);
	await postMessageToSW({
		type: MessageType.SET_INTERCEPTS,
		config: interceptConfig
	});
}

/**
 * Build IndexedDB intercept config with template substitution
 */
function buildIndexedDBConfig(config: IndexedDBConfig, metadata: AppMetadata): IndexedDBInterceptConfig {
	const intercepts = config.intercepts ?? [];

	return {
		database: config.database,
		intercepts: intercepts.map((entry) => ({
			...entry,
			value: substituteTemplates(entry.value, metadata)
		}))
	};
}

/**
 * Build localStorage intercept config with template substitution
 */
function buildLocalStorageConfig(config: LocalStorageConfig, metadata: AppMetadata): LocalStorageInterceptConfig {
	const intercepts = config.intercepts ?? [];

	return {
		intercepts: intercepts.map((entry) => {
			const result: LocalStorageInterceptConfig['intercepts'][0] = {
				key: entry.key
			};

			if (entry.value !== undefined) {
				result.value = substituteTemplates(entry.value, metadata);
			}

			if (entry.jsonPatch) {
				result.jsonPatch = {};
				for (const [path, value] of Object.entries(entry.jsonPatch)) {
					result.jsonPatch[path] = substituteTemplates(value, metadata);
				}
			}

			return result;
		})
	};
}

/**
 * Post a message to the service worker and wait for acknowledgment.
 * Uses MessageChannel for request-response pattern.
 */
async function postMessageToSW(message: unknown): Promise<void> {
	const registration = await navigator.serviceWorker.ready;
	const sw = registration.active;
	if (!sw) return;

	return new Promise<void>((resolve) => {
		const channel = new MessageChannel();

		channel.port1.onmessage = () => {
			resolve();
		};

		sw.postMessage(message, [channel.port2]);

		// Fallback timeout in case SW doesn't respond
		setTimeout(resolve, 100);
	});
}

/**
 * Write IndexedDB entries from config (writes field, or legacy entries field).
 * Writes to existing stores only - if store doesn't exist, entry is skipped.
 */
async function writeIndexedDBEntries(config: IndexedDBConfig, metadata: AppMetadata): Promise<void> {
	const entries = (config.writes ?? []).map((entry) => ({
		...entry,
		value: substituteTemplates(entry.value, metadata)
	}));

	if (entries.length === 0) {
		return;
	}

	const db = await openDatabase(config.database);
	try {
		await writeEntries(db, entries);
	} finally {
		db.close();
	}
}

function openDatabase(name: string): Promise<IDBDatabase> {
	return new Promise((resolve, reject) => {
		const request = indexedDB.open(name);
		request.onerror = () => reject(request.error);
		request.onsuccess = () => resolve(request.result);
	});
}

/**
 * Write entries to object stores (always overwrites, skips if store doesn't exist)
 */
async function writeEntries(db: IDBDatabase, entries: IndexedDBEntry[]): Promise<void> {
	for (const entry of entries) {
		// Skip if store doesn't exist yet (app will create it on first load)
		if (!db.objectStoreNames.contains(entry.store)) {
			continue;
		}

		await writeEntry(db, entry);
	}
}

/**
 * Write a single entry (always overwrites)
 */
function writeEntry(db: IDBDatabase, entry: IndexedDBEntry): Promise<void> {
	return new Promise((resolve, reject) => {
		const tx = db.transaction([entry.store], 'readwrite');
		const store = tx.objectStore(entry.store);
		const request = store.put(entry.value, entry.key);

		request.onsuccess = () => resolve();
		request.onerror = () => reject(request.error);
	});
}

/**
 * Substitute template variables in a string.
 * Replaces {{key}} with the corresponding value from metadata.
 */
function substituteTemplates(value: string, metadata: AppMetadata): string {
	return value.replace(/\{\{(\w+)\}\}/g, (match, key) => {
		if (key in metadata) {
			return String(metadata[key]);
		}
		return match; // Keep original if no matching key
	});
}
