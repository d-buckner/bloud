<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { browser } from '$app/environment';
	import { goto } from '$app/navigation';
	import { resolve } from '$app/paths';
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
	import {
		appView,
		enabledWidgetIds,
		gridApps,
		homeLoading,
		homeSummary,
		opensProgressInsteadOfLaunching,
		select,
		useAppActions,
		useExternalAppActions
	} from '$lib/store';
	import { fetchExternalApps, type ExternalApp } from '$lib/clients/settingsClient';
	import { getAppUrl } from '$lib/utils/appUrl';

	const installed = select(gridApps);

	const appActions = useAppActions();
	const externalActions = useExternalAppActions();
	const summary = select(homeSummary);
	const loading = select(homeLoading);
	const readError = select((s) => s.apps.error);
	const launchers = select((s) => s.launchers.items);
	const widgets = select(enabledWidgetIds);

	// The Add menu. One control for the two things a home screen can gain, rather
	// than two buttons whose labels both start with "Add".
	let addMenuOpen = $state(false);
	let addMenuEl = $state<HTMLDivElement | null>(null);

	function closeAddMenu(event: MouseEvent) {
		if (addMenuEl && event.target instanceof Node && addMenuEl.contains(event.target)) return;
		addMenuOpen = false;
	}

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

	let externalMenuLauncher = $derived(launchers.current.find((l) => l.id === externalMenuId));

	// Live reference: the modal reads the app through the store rather than
	// holding the object the user clicked, so status and progress keep updating.
	const installModalView = select((s) =>
		installModalApp ? appView(s, installModalApp.catalog_id) : null
	);
	let installModalLiveApp = $derived(installModalView.current?.app ?? null);

	let mounted = $state(false);

	/** One-line summary of what is on the grid, shown under the title. */
	let subtitle = $derived.by(() => {
		if (!mounted || loading.current) return 'Loading…';
		if (readError.current) return 'Could not reach the host agent';
		const counts = summary.current;
		const noun = (count: number, word: string) => `${count} ${word}${count === 1 ? '' : 's'}`;
		return [noun(counts.apps, 'app'), noun(counts.launchers, 'launcher'), noun(counts.widgets, 'widget')].join(
			' · '
		);
	});

	onMount(() => {
		mounted = true;
		document.addEventListener('click', closeAddMenu);
		return () => document.removeEventListener('click', closeAddMenu);
	});

	// Clicking an in-flight or unhealthy tile opens the live install view
	// (investigation is the point); clicking a running tile opens the app.
	function handleAppClick(app: App) {
		if (!browser) return;
		if (opensProgressInsteadOfLaunching(app)) {
			installModalApp = app;
			return;
		}
		if (app.status === AppStatus.Uninstalling) return;

		window.open(getAppUrl(app.catalog_id), '_blank');
	}

	async function handleRetryInstall(appName: string) {
		const result = await appActions.install(appName);
		if (!result.ok) console.error('Retry install failed:', result.error);
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

	function doRemoveExternalApp() {
		if (!externalRemoveId) return;
		// The store owns the request and the toast. The tile leaves when the
		// external list is re-read, which `onsaved` drives.
		void externalActions.remove(externalRemoveId);
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
		const result = await appActions.uninstall(appName);
		if (!result.ok) console.error('Uninstall failed:', result.error);
	}

	async function doRename(appName: string, newDisplayName: string) {
		const result = await appActions.rename(appName, newDisplayName);
		if (!result.ok) console.error('Rename failed:', result.error);
	}

	let isEmpty = $derived(summary.current.empty);

	// The catalog is a modal now, so the entry is a navigation to the route that
	// renders it. The URL stays shareable and the browser Back button closes the
	// modal, which a purely local `open` flag would not give.
	function openCatalog() {
		void goto(resolve('/catalog'));
	}
</script>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>Home</h1>
			<p class="subtitle">{subtitle}</p>
		</div>
		<!-- One Add control with a menu, not two buttons whose labels both start
		     with "Add". Each item puts something new on the grid, and an external
		     app is as much an addition as a widget is, so the menu names the kind
		     and the button names the action. -->
		<div class="header-actions">
			<Button variant="secondary" size="sm" onclick={openCatalog}>
				<Icon name="store" size={15} />
				Browse catalog
			</Button>
			<div class="add-menu" bind:this={addMenuEl}>
				<Button
					variant="secondary"
					size="sm"
					aria-haspopup="menu"
					aria-expanded={addMenuOpen}
					onclick={() => (addMenuOpen = !addMenuOpen)}
					onkeydown={(event) => {
						if (event.key === 'Escape') addMenuOpen = false;
					}}
				>
					<Icon name="plus" size={15} />
					Add
				</Button>
				{#if addMenuOpen}
					<div class="add-menu-list" role="menu" aria-label="Add to home">
						<button
							class="add-menu-item"
							type="button"
							role="menuitem"
							onclick={() => {
								addMenuOpen = false;
								showWidgetPicker = true;
							}}
						>
							<Icon name="grid" size={16} />
							Widget
						</button>
						<button
							class="add-menu-item"
							type="button"
							role="menuitem"
							onclick={() => {
								addMenuOpen = false;
								showExternalAppModal = true;
							}}
						>
							<Icon name="external-link" size={16} />
							External app
						</button>
					</div>
				{/if}
			</div>
		</div>
	</header>

	{#if !mounted || loading.current}
		<LoadingGrid />
	{:else if readError.current}
		<ErrorState message={readError.current} />
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

<ExternalAppModal
	open={showExternalAppModal}
	onclose={() => (showExternalAppModal = false)}
	onsaved={() => void refreshExternalApps()}
/>

<style>
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
		flex-wrap: wrap;
	}

	.add-menu {
		position: relative;
	}

	.add-menu-list {
		position: absolute;
		top: calc(100% + 4px);
		right: 0;
		z-index: 30;
		display: flex;
		flex-direction: column;
		min-width: 190px;
		padding: var(--space-xs);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-popover);
	}

	.add-menu-item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-md);
		background: none;
		border: none;
		border-radius: var(--radius-sm);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		color: var(--color-text);
		text-align: left;
		cursor: pointer;
	}

	.add-menu-item:hover {
		background: var(--color-bg-subtle);
	}
</style>
