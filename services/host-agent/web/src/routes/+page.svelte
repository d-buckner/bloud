<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';
	import GridStackGrid from '$lib/components/GridStackGrid.svelte';
	import AppContextMenu from '$lib/components/AppContextMenu.svelte';
	import ClientAccessModal from '$lib/components/ClientAccessModal.svelte';
	import LoadingGrid from '$lib/components/LoadingGrid.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import UninstallModal from '$lib/components/UninstallModal.svelte';
	import RenameModal from '$lib/components/RenameModal.svelte';
	import AppInstallModal from '$lib/components/AppInstallModal.svelte';
	import WidgetPicker from '$lib/widgets/WidgetPicker.svelte';
	import Button from '$lib/components/Button.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { AppStatus, type App } from '$lib/types';
	import { visibleApps as apps, loading, error } from '$lib/stores/apps';
	import { enabledWidgetIds } from '$lib/stores/grid';
	import { installApp, uninstallApp, renameApp } from '$lib/clients/appFacade';
	import { getAppUrl } from '$lib/utils/appUrl';

	// Clicking an in-flight or unhealthy tile opens the live install view
	// (investigation is the point); clicking a running tile opens the app.
	const INSTALL_VIEW_STATUSES = new Set<string>([
		AppStatus.Error,
		AppStatus.Installing,
		AppStatus.Starting,
		AppStatus.Failed
	]);

	// Context menu state
	let contextMenuApp = $state<App | null>(null);
	let contextMenuPos = $state({ x: 0, y: 0 });

	// Modal state
	let uninstallAppName = $state<string | null>(null);
	let renameAppName = $state<string | null>(null);
	let renameCurrentDisplayName = $state<string>('');
	let clientAccessApp = $state<App | null>(null);
	let showWidgetPicker = $state(false);
	let installModalApp = $state<App | null>(null);

	// Live reference: re-resolve from the store each render so the modal
	// tracks status/progress updates for the app the user clicked.
	let installModalLiveApp = $derived.by(() => {
		const id = installModalApp?.catalog_id;
		if (!id) return null;
		return $apps.find((a) => a.catalog_id === id) ?? installModalApp;
	});

	let mounted = $state(false);

	/** One-line summary of what is on the grid, shown under the title. */
	let subtitle = $derived.by(() => {
		if (!mounted || $loading) return 'Loading…';
		if ($error) return 'Could not reach the host agent';
		const appCount = $apps.length;
		const widgetCount = $enabledWidgetIds.length;
		const noun = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`;
		return [noun(appCount, 'app'), noun(widgetCount, 'widget')].join(' · ');
	});

	onMount(() => {
		mounted = true;
	});

	function handleAppClick(app: App) {
		if (!browser) return;
		if (INSTALL_VIEW_STATUSES.has(app.status)) {
			// Keep a live reference so the modal tracks status/progress updates.
			installModalApp = app;
			return;
		}
		if (app.status === AppStatus.Uninstalling) return;

		window.open(getAppUrl(app.catalog_id), '_blank');
	}

	async function handleRetryInstall(appName: string) {
		try {
			await installApp(appName);
		} catch (err) {
			console.error('Retry install failed:', err);
		}
	}

	function handleContextMenu(e: MouseEvent, app: App) {
		e.preventDefault();
		contextMenuApp = app;
		contextMenuPos = { x: e.clientX, y: e.clientY };
	}

	// Context menu handlers
	function handleRenameClick(app: App) {
		renameAppName = app.catalog_id;
		renameCurrentDisplayName = app.display_name;
	}

	function handleUninstallClick(app: App) {
		uninstallAppName = app.catalog_id;
	}

	function handleClientAccessClick(app: App) {
		clientAccessApp = app;
	}

	// Modal actions
	async function doUninstall(appName: string) {
		try {
			await uninstallApp(appName);
		} catch (err) {
			console.error('Uninstall failed:', err);
		}
	}

	async function doRename(appName: string, newDisplayName: string) {
		const result = await renameApp(appName, newDisplayName);
		if (!result.success) {
			console.error('Rename failed:', result.error);
		}
	}

	let isEmpty = $derived($apps.length === 0 && $enabledWidgetIds.length === 0);
</script>

<svelte:head>
	<title>Home · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>Home</h1>
			<p class="subtitle">{subtitle}</p>
		</div>
		<Button variant="secondary" size="sm" onclick={() => (showWidgetPicker = true)}>
			<Icon name="plus" size={15} />
			Add widget
		</Button>
	</header>

	{#if !mounted || $loading}
		<LoadingGrid />
	{:else if $error}
		<ErrorState message={$error} />
	{:else if isEmpty}
		<EmptyState />
	{:else}
		<GridStackGrid onAppClick={handleAppClick} onAppContextMenu={handleContextMenu} />
	{/if}
</div>

<AppContextMenu
	app={contextMenuApp}
	position={contextMenuPos}
	onRename={handleRenameClick}
	onUninstall={handleUninstallClick}
	onClientAccess={handleClientAccessClick}
	onClose={() => (contextMenuApp = null)}
/>

<ClientAccessModal
	appName={clientAccessApp?.catalog_id ?? null}
	displayName={clientAccessApp?.display_name ?? ''}
	onclose={() => (clientAccessApp = null)}
/>

<UninstallModal
	appName={uninstallAppName}
	onclose={() => (uninstallAppName = null)}
	onuninstall={doUninstall}
/>

<RenameModal
	appName={renameAppName}
	currentDisplayName={renameCurrentDisplayName}
	onclose={() => (renameAppName = null)}
	onrename={doRename}
/>

<AppInstallModal
	app={installModalLiveApp}
	onclose={() => (installModalApp = null)}
	onretry={handleRetryInstall}
/>

<WidgetPicker open={showWidgetPicker} onclose={() => (showWidgetPicker = false)} />

<style>
	.page {
		width: 100%;
		/* Wide enough for six comfortable columns, narrow enough that tiles
		   stay tile-sized on a large display. */
		max-width: 1300px;
		margin: 0 auto;
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		display: flex;
		align-items: flex-end;
		justify-content: space-between;
		gap: var(--space-lg);
		margin-bottom: var(--space-xl);
		padding-bottom: var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0;
		font-family: var(--font-sans);
		font-size: 0.8125rem;
		color: var(--color-text-muted);
	}

	@media (max-width: 768px) {
		.page {
			padding: var(--space-xl) var(--space-md);
		}

		.page-header {
			flex-direction: column;
			align-items: flex-start;
			gap: var(--space-md);
		}
	}
</style>
