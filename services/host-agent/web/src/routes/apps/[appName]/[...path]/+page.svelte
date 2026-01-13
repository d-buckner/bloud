<script lang="ts">
	import { type App, type CatalogApp, type IndexedDBEntry, AppStatus } from '$lib/types';
	import type { ProtectedEntry } from '../../../../service-worker/types';
	import { openApps } from '$lib/stores/openApps';
	import Icon from '$lib/components/Icon.svelte';
	import { bootstrap, setActiveApp, setProtectedEntries } from '$lib/bootstrap';
	import { executeBootstrap, type AppMetadata } from '$lib/appConfig';
	import { onDestroy } from 'svelte';
	import { goto } from '$app/navigation';

	let { data } = $props();

	let app = $state<App | null>(null);
	let loading = $state(true);
	let iframeLoading = $state(true);
	let error = $state<string | null>(null);
	let iframeEl = $state<HTMLIFrameElement | null>(null);
	let swReady = $state(false);
	let configReady = $state(false);

	let appName = $derived(data.appName);

	// Track iframe src separately from URL - we control when to reload
	let iframePath = $state<string>('');
	let isSyncingUrl = false; // Flag to ignore our own goto() calls

	// Update iframe path when route changes, but NOT from our own URL sync
	$effect(() => {
		// Access appName to track it - we want to reset iframePath when app changes
		void appName;
		const currentPath = data.path;

		if (isSyncingUrl) {
			// This navigation came from our URL sync - ignore it
			return;
		}

		// User-initiated navigation (or app change) - update iframe src
		iframePath = currentPath;
	});

	// Wait for service worker before rendering iframe
	bootstrap().then(async () => {
		// Set active app and wait for it to be set before rendering iframe
		if (appName) {
			await setActiveApp(appName);
		}
		swReady = true;
	});

	// Clear active app when component is destroyed (navigating away)
	onDestroy(() => {
		setActiveApp(null);
	});

	/**
	 * Extract protected entries from bootstrap config and apply template substitution.
	 */
	function getProtectedEntries(
		config: CatalogApp,
		metadata: AppMetadata
	): ProtectedEntry[] {
		const indexedDB = config.bootstrap?.indexedDB;
		if (!indexedDB?.entries) {
			return [];
		}

		return indexedDB.entries
			.filter((entry: IndexedDBEntry) => entry.protected)
			.map((entry: IndexedDBEntry) => ({
				database: indexedDB.database,
				store: entry.store,
				key: entry.key,
				// Apply template substitution (same as executeBootstrap)
				value: entry.value.replace(/\{\{(\w+)\}\}/g, (match, key) => {
					if (key in metadata) {
						return String(metadata[key as keyof AppMetadata]);
					}
					return match;
				})
			}));
	}

	async function loadApp(currentAppName: string) {
		try {
			// Fetch installed apps
			const installedRes = await fetch('/api/apps/installed');
			if (!installedRes.ok) throw new Error('Failed to fetch installed apps');

			const apps: App[] = await installedRes.json();
			const foundApp = apps.find((a) => a.name === currentAppName);

			if (!foundApp) {
				error = `App "${currentAppName}" is not installed`;
				return;
			}

			app = foundApp;
			openApps.open(foundApp);

			// Fetch app metadata for bootstrap config
			const metadataRes = await fetch(`/api/apps/${currentAppName}/metadata`);
			if (!metadataRes.ok) throw new Error('Failed to fetch app metadata');
			const catalogApp: CatalogApp = await metadataRes.json();

			// Build template variables (catalog metadata + computed runtime values)
			const appMetadata: AppMetadata = {
				...catalogApp,
				origin: window.location.origin,
				embedUrl: `${window.location.origin}/embed/${currentAppName}`
			};

			// Send protected entries to SW BEFORE loading iframe
			// This ensures the injection script is ready when the first HTML is fetched
			const protectedEntries = getProtectedEntries(catalogApp, appMetadata);
			if (protectedEntries.length > 0) {
				await setProtectedEntries(currentAppName, protectedEntries);
			}

			// Execute bootstrap configuration (writes to IndexedDB)
			const result = await executeBootstrap(currentAppName, catalogApp.bootstrap, appMetadata);

			if (!result.success) {
				error = `Bootstrap failed: ${result.error}`;
				return;
			}

			configReady = true;
		} catch (err) {
			error = err instanceof Error ? err.message : 'Failed to connect to API';
		} finally {
			loading = false;
		}
	}

	// Load app data when appName changes
	$effect(() => {
		const currentAppName = appName;
		if (!currentAppName) {
			loading = false;
			setActiveApp(null);
			return;
		}

		// Notify SW of active app immediately (before loading app data)
		setActiveApp(currentAppName);

		// Reset state for new app
		loading = true;
		iframeLoading = true;
		configReady = false;
		error = null;

		loadApp(currentAppName);
	});

	function getIframeSrc(name: string, path: string): string {
		// App content is served from /embed/{appName}/ path
		// Same-origin so cookies work in iframes
		const basePath = `/embed/${name}/`;
		return path ? `${basePath}${path}` : basePath;
	}

	function handleIframeLoad() {
		iframeLoading = false;
		syncUrlFromIframe();
	}

	function syncUrlFromIframe() {
		if (!iframeEl?.contentWindow || !appName) return;

		try {
			// Get the iframe's current path (same-origin so we can access it)
			const iframePath = iframeEl.contentWindow.location.pathname;
			const iframeSearch = iframeEl.contentWindow.location.search;

			// Extract the app-relative path from /embed/{appName}/...
			const embedPrefix = `/embed/${appName}/`;
			if (iframePath.startsWith(embedPrefix)) {
				const relativePath = iframePath.slice(embedPrefix.length) + iframeSearch;
				const newUrl = `/apps/${appName}/${relativePath}`;

				// Save the path to the store for tab restoration
				openApps.setPath(appName, relativePath);

				// Only update browser URL if different from current
				if (window.location.pathname + window.location.search !== newUrl) {
					isSyncingUrl = true;
					goto(newUrl, { replaceState: true, noScroll: true }).finally(() => {
						isSyncingUrl = false;
					});
				}
			}
		} catch (e) {
			// Cross-origin error - can't access iframe location
			console.warn('Could not sync URL from iframe:', e);
		}
	}
</script>

<svelte:head>
	<title>{app?.display_name ?? appName ?? 'App'} - Bloud</title>
</svelte:head>

{#if !appName}
	<!-- Navigation transition - render nothing -->
{:else if loading || !swReady}
	<div class="status-container">
		<div class="spinner"></div>
		<p>Loading...</p>
	</div>
{:else if error}
	<div class="status-container">
		<div class="status-icon error">
			<Icon name="warning" size={32} />
		</div>
		<p class="error-text">{error}</p>
		<a href="/" class="back-link">Back to Apps</a>
	</div>
{:else if app && app.status === AppStatus.Running && app.port && swReady && configReady}
	<div class="iframe-wrapper">
		{#if iframeLoading}
			<div class="iframe-loading">
				<div class="spinner"></div>
				<p>Connecting to {app.display_name}...</p>
			</div>
		{/if}
		<iframe
			bind:this={iframeEl}
			class="app-iframe"
			src={getIframeSrc(app.name, iframePath)}
			title={app.display_name}
			onload={handleIframeLoad}
		></iframe>
	</div>
{:else if app}
	<div class="status-container">
		{#if app.status === AppStatus.Installing}
			<div class="spinner large"></div>
			<p>Installing {app.display_name}</p>
			<span class="status-text">This may take a few minutes...</span>
		{:else if app.status === AppStatus.Starting}
			<div class="spinner large"></div>
			<p>Starting {app.display_name}</p>
			<span class="status-text">Waiting for health check...</span>
		{:else if app.status === AppStatus.Stopped}
			<div class="status-icon stopped">
				<Icon name="stop" size={32} />
			</div>
			<p>{app.display_name} is stopped</p>
		{:else if app.status === AppStatus.Error || app.status === AppStatus.Failed}
			<div class="status-icon error">
				<Icon name="warning" size={32} />
			</div>
			<p>{app.display_name} failed to start</p>
			<span class="status-text">Check logs for more details</span>
		{:else}
			<p>App is not available</p>
			<span class="status-text">Status: {app.status}</span>
		{/if}
	</div>
{:else}
	<div class="status-container">
		<p>App not found</p>
		<a href="/" class="back-link">Back to Apps</a>
	</div>
{/if}

<style>
	.iframe-wrapper {
		position: relative;
		width: 100%;
		height: 100vh;
	}

	.app-iframe {
		width: 100%;
		height: 100%;
		border: none;
	}

	.iframe-loading {
		position: absolute;
		top: 0;
		left: 0;
		right: 0;
		bottom: 0;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-md);
		background: var(--color-bg);
		z-index: 1;
	}

	.iframe-loading p {
		margin: 0;
		color: var(--color-text-muted);
	}

	.status-container {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		height: 100vh;
		gap: var(--space-sm);
		color: var(--color-text-muted);
	}

	.status-container p {
		margin: 0;
		font-size: 1.125rem;
		color: var(--color-text);
	}

	.status-text {
		font-size: 0.875rem;
	}

	.spinner {
		width: 32px;
		height: 32px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	.spinner.large {
		width: 40px;
		height: 40px;
		border-width: 3px;
		margin-bottom: var(--space-sm);
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.status-icon {
		margin-bottom: var(--space-sm);
	}

	.status-icon.stopped {
		color: #F59E0B;
	}

	.status-icon.error {
		color: var(--color-error);
	}

	.error-text {
		color: var(--color-error);
	}

	.back-link {
		margin-top: var(--space-md);
		color: var(--color-accent);
		text-decoration: none;
	}

	.back-link:hover {
		text-decoration: underline;
	}
</style>
