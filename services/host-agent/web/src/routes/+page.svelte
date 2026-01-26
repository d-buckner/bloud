<script lang="ts">
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';
	import UnifiedGrid from '$lib/components/UnifiedGrid.svelte';
	import AppContextMenu from '$lib/components/AppContextMenu.svelte';
	import LoadingGrid from '$lib/components/LoadingGrid.svelte';
	import EmptyState from '$lib/components/EmptyState.svelte';
	import ErrorState from '$lib/components/ErrorState.svelte';
	import UninstallModal from '$lib/components/UninstallModal.svelte';
	import LogsModal from '$lib/components/LogsModal.svelte';
	import RenameModal from '$lib/components/RenameModal.svelte';
	import WidgetPicker from '$lib/widgets/WidgetPicker.svelte';
	import { AppStatus, type App } from '$lib/types';
	import { visibleApps as apps, loading, error } from '$lib/stores/apps';
	import { uninstallApp, renameApp } from '$lib/services/appFacade';
	import { openApp } from '$lib/services/navigation';
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
	let showWidgetPicker = $state(false);

	let mounted = $state(false);

	onMount(() => {
		mounted = true;
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

		openApp(app);
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

	// Derived state for empty check
	let isEmpty = $derived(
		$apps.length === 0 && $layout.filter((i) => i.type === 'widget').length === 0
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
		<UnifiedGrid
			onAppClick={handleAppClick}
			onAppContextMenu={handleContextMenu}
			onAddWidget={() => (showWidgetPicker = true)}
		/>
	{/if}
</div>

<AppContextMenu
	app={contextMenuApp}
	position={contextMenuPos}
	onViewLogs={handleViewLogs}
	onRename={handleRenameClick}
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

<WidgetPicker open={showWidgetPicker} onclose={() => (showWidgetPicker = false)} />

<style>
	.launcher {
		display: flex;
		flex-direction: column;
		min-height: 100vh;
		padding: var(--space-2xl);
	}

	@media (max-width: 768px) {
		.launcher {
			padding: var(--space-xl);
		}
	}
</style>
