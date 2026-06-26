<script lang="ts">
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';
	import GridStackGrid from '$lib/components/GridStackGrid.svelte';
	import AppContextMenu from '$lib/components/AppContextMenu.svelte';
	import RemoteAppCard from '$lib/components/RemoteAppCard.svelte';
	import LoadingGrid from '$lib/components/LoadingGrid.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import UninstallModal from '$lib/components/UninstallModal.svelte';
	import LogsModal from '$lib/components/LogsModal.svelte';
	import RenameModal from '$lib/components/RenameModal.svelte';
	import ShareModal from '$lib/components/ShareModal.svelte';
	import WidgetPicker from '$lib/widgets/WidgetPicker.svelte';
	import { AppStatus, type App, type RemoteApp } from '$lib/types';
	import { visibleApps as apps, loading, error } from '$lib/stores/apps';
	import { uninstallApp, renameApp } from '$lib/services/appFacade';
	import { getAppUrl } from '$lib/utils/appUrl';
	import { getRemoteAppUrl } from '$lib/utils/appUrl';
	import { fetchRemoteApps, removeRemoteApp } from '$lib/clients/remoteAppClient';
	import { layout } from '$lib/stores/layout';

	// Context menu state
	let contextMenuApp = $state<App | null>(null);
	let contextMenuPos = $state({ x: 0, y: 0 });

	// Modal state
	let uninstallAppName = $state<string | null>(null);
	let logsAppName = $state<string | null>(null);
	let logsDisplayName = $state<string>('');
	let renameAppName = $state<string | null>(null);
	let renameCurrentDisplayName = $state<string>('');
	let shareApp = $state<App | null>(null);
	let removeRemoteAppTarget = $state<RemoteApp | null>(null);
	let showWidgetPicker = $state(false);

	// Remote apps
	let remoteApps = $state<RemoteApp[]>([]);

	let mounted = $state(false);

	onMount(async () => {
		mounted = true;
		try {
			remoteApps = await fetchRemoteApps();
		} catch {
			// Remote apps are optional — don't block the page
		}
	});

	function handleAppClick(app: App) {
		if (!browser) return;
		if (app.status === AppStatus.Error) return;
		if (
			app.status === AppStatus.Installing ||
			app.status === AppStatus.Starting ||
			app.status === AppStatus.Uninstalling
		)
			return;

		const path = app.sso_launch_path ?? '';
		window.open(getAppUrl(app.name, path), '_blank');
	}

	function handleContextMenu(e: MouseEvent, app: App) {
		e.preventDefault();
		contextMenuApp = app;
		contextMenuPos = { x: e.clientX, y: e.clientY };
	}

	// Context menu handlers
	function handleViewLogs(app: App) {
		logsAppName = app.name;
		logsDisplayName = app.display_name;
	}

	function handleRenameClick(app: App) {
		renameAppName = app.name;
		renameCurrentDisplayName = app.display_name;
	}

	function handleShareClick(app: App) {
		shareApp = app;
	}

	function handleUninstallClick(app: App) {
		uninstallAppName = app.name;
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

	function handleRemoteAppClick(app: RemoteApp) {
		if (!browser) return;
		window.open(getRemoteAppUrl(app.app_id, app.host_label), '_blank');
	}

	function handleRemoveRemoteApp(app: RemoteApp) {
		removeRemoteAppTarget = app;
	}

	async function doRemoveRemoteApp(id: string) {
		try {
			await removeRemoteApp(id);
			remoteApps = remoteApps.filter((a) => a.id !== id);
		} catch (err) {
			console.error('Remove remote app failed:', err);
		}
	}

	// Derived state for empty check
	let isEmpty = $derived(
		$apps.length === 0 && remoteApps.length === 0 && $layout.filter((i) => i.type === 'widget').length === 0
	);
</script>

<svelte:head>
	<title>Apps · Bloud</title>
</svelte:head>

<div class="launcher">
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
			onAddWidget={() => (showWidgetPicker = true)}
		/>

		{#if remoteApps.length > 0}
			<section class="remote-apps-section">
				<h2 class="section-title">Shared Apps</h2>
				<div class="remote-apps-grid">
					{#each remoteApps as app (app.id)}
						<RemoteAppCard {app} onclick={() => handleRemoteAppClick(app)} onremove={handleRemoveRemoteApp} />
					{/each}
				</div>
			</section>
		{/if}
	{/if}
</div>

<AppContextMenu
	app={contextMenuApp}
	position={contextMenuPos}
	onViewLogs={handleViewLogs}
	onRename={handleRenameClick}
	onShare={handleShareClick}
	onUninstall={handleUninstallClick}
	onClose={() => (contextMenuApp = null)}
/>

<UninstallModal
	appName={uninstallAppName}
	onclose={() => (uninstallAppName = null)}
	onuninstall={doUninstall}
/>

<LogsModal appName={logsAppName} displayName={logsDisplayName} onclose={() => (logsAppName = null)} />

<RenameModal
	appName={renameAppName}
	currentDisplayName={renameCurrentDisplayName}
	onclose={() => (renameAppName = null)}
	onrename={doRename}
/>

<ShareModal app={shareApp} onclose={() => (shareApp = null)} />

<UninstallModal
	appName={removeRemoteAppTarget ? `${removeRemoteAppTarget.app_name} (${removeRemoteAppTarget.host_label})` : null}
	onclose={() => (removeRemoteAppTarget = null)}
	onuninstall={() => {
		if (removeRemoteAppTarget) doRemoveRemoteApp(removeRemoteAppTarget.id);
	}}
/>

<WidgetPicker open={showWidgetPicker} onclose={() => (showWidgetPicker = false)} />

<style>
	.launcher {
		display: flex;
		flex-direction: column;
		min-height: 100vh;
		padding: var(--space-2xl);
	}

	.remote-apps-section {
		margin-top: var(--space-2xl);
		padding-top: var(--space-xl);
		border-top: 1px solid var(--color-border);
	}

	.section-title {
		margin: 0 0 var(--space-lg) 0;
		font-size: 1rem;
		font-weight: 500;
		color: var(--color-text-muted);
	}

	.remote-apps-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
		gap: var(--space-md);
	}

	@media (max-width: 768px) {
		.launcher {
			padding: var(--space-xl);
		}

		.remote-apps-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
