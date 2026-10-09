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
	import ExternalAppModal from '$lib/components/ExternalAppModal.svelte';
	import ExternalAppContextMenu from '$lib/components/ExternalAppContextMenu.svelte';
	import ExternalAppConfigModal from '$lib/components/ExternalAppConfigModal.svelte';
	import ExternalAppRemoveModal from '$lib/components/ExternalAppRemoveModal.svelte';
	import WidgetPicker from '$lib/widgets/WidgetPicker.svelte';
	import Button from '$lib/components/Button.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { AppStatus, type App } from '$lib/types';
	import { visibleApps as apps, loading, error } from '$lib/stores/apps';
	import { launchers } from '$lib/stores/launchers';
	import { enabledWidgetIds } from '$lib/stores/grid';
	import { installApp, uninstallApp, renameApp } from '$lib/clients/appFacade';
	import { fetchExternalApps, removeExternalApp, type ExternalApp } from '$lib/clients/settingsClient';
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

	// External tiles (launchers and remote installs) are not installed apps, so
	// they get their own menu and their own two modals rather than borrowing the
	// lifecycle ones. The menu carries only the record id and what the tile
	// already knows; the full record is fetched for the one action that needs it.
	let externalMenuId = $state<string | null>(null);
	let externalMenuPos = $state({ x: 0, y: 0 });
	let externalConfigApp = $state<ExternalApp | null>(null);
	let externalRemoveTarget = $state<{ name: string; isProvider: boolean } | null>(null);
	let externalRemoveId = $state<string | null>(null);
	let showExternalAppModal = $state(false);

	let externalRecords = $state<ExternalApp[]>([]);
	let externalRecordsInFlight: Promise<ExternalApp[]> | null = null;

	let externalMenuLauncher = $derived($launchers.find((l) => l.id === externalMenuId));

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
		const launcherCount = $launchers.length;
		const widgetCount = $enabledWidgetIds.length;
		const noun = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`;
		return [noun(appCount, 'app'), noun(launcherCount, 'launcher'), noun(widgetCount, 'widget')].join(' · ');
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

	/**
	 * Read the external records, coalescing concurrent calls onto one request.
	 *
	 * The list is fetched rather than widened into the home snapshot because the
	 * config form needs fields the tile never uses: the stored contract values
	 * and which contracts hold a credential. A second source of truth on the
	 * same screen is the thing to avoid here, not a second request.
	 */
	function refreshExternalApps(): Promise<ExternalApp[]> {
		if (externalRecordsInFlight) return externalRecordsInFlight;
		externalRecordsInFlight = fetchExternalApps()
			.then((records) => {
				externalRecords = records;
				return records;
			})
			.catch((err) => {
				console.error('Could not load external apps:', err);
				return externalRecords;
			})
			.finally(() => {
				externalRecordsInFlight = null;
			});
		return externalRecordsInFlight;
	}

	function handleLauncherContextMenu(e: MouseEvent, itemId: string) {
		e.preventDefault();
		// Start the read now, not on the click that follows. The menu itself needs
		// nothing but the tile, so it opens instantly; by the time the pointer
		// reaches a menu item the record is already in.
		void refreshExternalApps();
		externalMenuId = itemId;
		externalMenuPos = { x: e.clientX, y: e.clientY };
	}

	async function handleConfigureExternalApp(itemId: string) {
		const records = await refreshExternalApps();
		const record = records.find((a) => a.id === itemId);
		if (!record) {
			console.error('No external app record for', itemId);
			return;
		}
		externalConfigApp = record;
	}

	function handleRemoveExternalApp() {
		const launcher = externalMenuLauncher;
		externalRemoveId = externalMenuId;
		externalRemoveTarget = {
			name: launcher?.name ?? 'this app',
			// A tile stands for a catalog app exactly when it carries one; a bare
			// contract provider never reaches the grid at all.
			isProvider: !!launcher?.app
		};
	}

	async function doRemoveExternalApp() {
		if (!externalRemoveId) return;
		try {
			await removeExternalApp(externalRemoveId);
		} catch (err) {
			console.error('Remove failed:', err);
		}
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

	let isEmpty = $derived($apps.length === 0 && $launchers.length === 0 && $enabledWidgetIds.length === 0);
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
		<!-- Both carry the plus because the glyph names the action, not the kind of
		     thing added: each button puts something new on the grid, and an
		     external app is as much an addition as a widget is. -->
		<div class="header-actions">
			<Button variant="secondary" size="sm" onclick={() => (showWidgetPicker = true)}>
				<Icon name="plus" size={15} />
				Add widget
			</Button>
			<Button variant="secondary" size="sm" onclick={() => (showExternalAppModal = true)}>
				<Icon name="plus" size={15} />
				Add external app
			</Button>
		</div>
	</header>

	{#if !mounted || $loading}
		<LoadingGrid />
	{:else if $error}
		<ErrorState message={$error} />
	{:else if isEmpty}
		<EmptyState />
	{:else}
		<GridStackGrid
			onAppClick={handleAppClick}
			onAppContextMenu={handleContextMenu}
			onLauncherContextMenu={handleLauncherContextMenu}
		/>
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

<ExternalAppContextMenu
	itemId={externalMenuId}
	displayName={externalMenuLauncher?.name ?? 'external app'}
	isProvider={!!externalMenuLauncher?.app}
	position={externalMenuPos}
	onConfigure={handleConfigureExternalApp}
	onRemove={handleRemoveExternalApp}
	onClose={() => (externalMenuId = null)}
/>

<!-- Keyed on the record id so switching to a different record remounts the
     form instead of leaving the previous record's half-typed edits on screen. -->
{#key externalConfigApp?.id ?? null}
	<ExternalAppConfigModal
		app={externalConfigApp}
		onclose={() => (externalConfigApp = null)}
		onsaved={() => void refreshExternalApps()}
	/>
{/key}

<ExternalAppRemoveModal
	target={externalRemoveTarget}
	onclose={() => (externalRemoveTarget = null)}
	onremove={doRemoveExternalApp}
/>

<ExternalAppModal open={showExternalAppModal} onclose={() => (showExternalAppModal = false)} />

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

	/* The two add actions travel together. Without this wrapper the header has
	   three flex children under `justify-content: space-between`, which puts
	   one of them in the middle of the row: a button floating out over the
	   subtitle, unattached to either the title it does not belong to or the
	   sibling it does. Grouping them is what makes the pair read as one
	   cluster on the right instead of two strays. */
	.header-actions {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
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
