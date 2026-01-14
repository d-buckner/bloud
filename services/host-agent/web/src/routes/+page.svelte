<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { browser } from '$app/environment';
	import AppIcon from '$lib/components/AppIcon.svelte';
	import UninstallModal from '$lib/components/UninstallModal.svelte';
	import LogsModal from '$lib/components/LogsModal.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { AppStatus, type App } from '$lib/types';
	import { visibleApps as apps, loading, error } from '$lib/stores/apps';
	import { uninstallApp as doUninstallApp } from '$lib/services/appLifecycle';
	import { openApp } from '$lib/services/navigation';
	import WidgetContainer from '$lib/widgets/WidgetContainer.svelte';


	let uninstallAppName = $state<string | null>(null);
	let logsAppName = $state<string | null>(null);
	let logsDisplayName = $state<string>('');
	let contextMenuApp = $state<App | null>(null);
	let contextMenuPos = $state({ x: 0, y: 0 });
	let mounted = $state(false);

	onMount(() => {
		mounted = true;
		document.addEventListener('click', closeContextMenu);
	});

	onDestroy(() => {
		if (browser) {
			document.removeEventListener('click', closeContextMenu);
		}
	});

	async function doUninstall(appName: string) {
		try {
			await doUninstallApp(appName);
		} catch (err) {
			console.error('Uninstall failed:', err);
		}
	}

	function handleAppClick(app: App) {
		if (!browser) return;
		if (app.status === AppStatus.Error) return;
		if (app.status === AppStatus.Installing || app.status === AppStatus.Starting || app.status === AppStatus.Uninstalling) return;

		// System apps (postgres, traefik, authentik) open in new tab
		// User-facing apps use embedded iframe viewer
		if (app.is_system) {
			if (app.port) {
				window.open(`http://${window.location.hostname}:${app.port}`, '_blank');
			}
			return;
		}

		// Use embedded AppViewer for user-facing apps
		openApp(app);
	}

	function handleContextMenu(e: MouseEvent, app: App) {
		e.preventDefault();
		contextMenuApp = app;
		contextMenuPos = { x: e.clientX, y: e.clientY };
	}

	function closeContextMenu() {
		contextMenuApp = null;
	}

	function handleUninstall() {
		if (contextMenuApp) {
			uninstallAppName = contextMenuApp.name;
			contextMenuApp = null;
		}
	}

	function handleOpenInNewTab() {
		if (!browser || !contextMenuApp?.port) return;
		window.open(`http://${window.location.hostname}:${contextMenuApp.port}`, '_blank');
		contextMenuApp = null;
	}

	function handleViewLogs() {
		if (contextMenuApp) {
			logsAppName = contextMenuApp.name;
			logsDisplayName = contextMenuApp.display_name;
			contextMenuApp = null;
		}
	}
</script>

<svelte:head>
	<title>Apps · Bloud</title>
</svelte:head>

<div class="launcher">
	<div class="launcher-content">
		{#if !mounted || $loading}
			<div class="app-grid">
				{#each Array(8) as _}
					<div class="app-slot skeleton">
						<div class="skeleton-icon"></div>
						<div class="skeleton-label"></div>
					</div>
				{/each}
			</div>
		{:else if $error}
			<div class="error-state">
				<p>{$error}</p>
			</div>
		{:else if $apps.length === 0}
			<div class="empty-state">
				<div class="empty-icon">
					<Icon name="grid" size={64} />
				</div>
				<h2>No apps yet</h2>
				<p>Browse the App Store to get started</p>
				<a href="/catalog" class="get-apps-btn">Get Apps</a>
			</div>
		{:else}
			<div class="app-grid">
				{#each $apps as app}
					{@const isInstalling = app.status === AppStatus.Installing || app.status === AppStatus.Starting}
					{@const isUninstalling = app.status === AppStatus.Uninstalling}
					{@const isBusy = isInstalling || isUninstalling}
					{@const hasError = app.status === AppStatus.Error}
					<button
						class="app-slot"
						class:installing={isBusy}
						class:error={hasError}
						onclick={() => handleAppClick(app)}
						oncontextmenu={(e) => handleContextMenu(e, app)}
					>
						<div class="app-icon-container">
							{#if isBusy}
								<div class="install-progress"></div>
							{/if}
							<AppIcon appName={app.name} displayName={app.display_name} size="lg" transparent={isBusy} />
							{#if hasError}
								<div class="error-badge">!</div>
							{/if}
						</div>
						<span class="app-label">{app.display_name}</span>
					</button>
				{/each}
			</div>
		{/if}
	</div>

	<!-- Widgets Section -->
	<section class="widgets">
		<WidgetContainer />
	</section>
</div>

<!-- Context Menu -->
{#if contextMenuApp}
	<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
	<div
		class="context-menu"
		style="left: {contextMenuPos.x}px; top: {contextMenuPos.y}px;"
		onclick={(e) => e.stopPropagation()}
	>
		<button class="context-item" onclick={handleOpenInNewTab}>
			<Icon name="external-link" size={16} />
			Open in New Tab
		</button>
		<button class="context-item" onclick={handleViewLogs}>
			<Icon name="terminal" size={16} />
			View Logs
		</button>
		<hr class="context-divider" />
		<button class="context-item danger" onclick={handleUninstall}>
			<Icon name="trash" size={16} />
			Remove App
		</button>
	</div>
{/if}

<UninstallModal
	appName={uninstallAppName}
	onclose={() => uninstallAppName = null}
	onuninstall={doUninstall}
/>

<LogsModal
	appName={logsAppName}
	displayName={logsDisplayName}
	onclose={() => logsAppName = null}
/>

<style>
	.launcher {
		display: flex;
		flex-direction: column;
		min-height: 100vh;
		padding: var(--space-2xl);
	}

	.launcher-content {
		display: flex;
		justify-content: center;
		padding-top: var(--space-3xl);
	}

	.app-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(88px, 88px));
		gap: var(--space-xl) var(--space-lg);
		justify-content: center;
		max-width: 600px;
	}

	.app-slot {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm);
		background: transparent;
		border: none;
		cursor: pointer;
		transition: transform 0.1s ease;
	}

	.app-slot:hover {
		transform: scale(1.05);
	}

	.app-slot:active {
		transform: scale(0.95);
	}

	.app-slot.installing {
		pointer-events: none;
	}

	.app-slot.error {
		cursor: default;
	}

	.app-icon-container {
		position: relative;
		width: 60px;
		height: 60px;
	}

	.app-icon-container :global(.app-icon) {
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
	}

	.install-progress {
		position: absolute;
		inset: -3px;
		border: 2.5px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 1s linear infinite;
	}


	.error-badge {
		position: absolute;
		top: -4px;
		right: -4px;
		width: 18px;
		height: 18px;
		background: var(--color-error);
		color: white;
		font-size: 12px;
		font-weight: 600;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.2);
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	.app-label {
		font-size: 11px;
		color: var(--color-text);
		text-align: center;
		max-width: 80px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		line-height: 1.2;
	}

	.app-slot.installing .app-label,
	.app-slot.error .app-label {
		color: var(--color-text-muted);
	}

	/* Skeleton */
	.app-slot.skeleton {
		pointer-events: none;
	}

	.skeleton-icon {
		width: 60px;
		height: 60px;
		border-radius: 14px;
		background: linear-gradient(90deg, var(--color-bg-subtle) 25%, var(--color-bg-elevated) 50%, var(--color-bg-subtle) 75%);
		background-size: 200% 100%;
		animation: shimmer 1.5s infinite;
	}

	.skeleton-label {
		width: 50px;
		height: 11px;
		border-radius: 4px;
		background: linear-gradient(90deg, var(--color-bg-subtle) 25%, var(--color-bg-elevated) 50%, var(--color-bg-subtle) 75%);
		background-size: 200% 100%;
		animation: shimmer 1.5s infinite;
		animation-delay: 0.1s;
	}

	@keyframes shimmer {
		0% { background-position: 200% 0; }
		100% { background-position: -200% 0; }
	}

	/* Empty state */
	.empty-state {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		gap: var(--space-md);
	}

	.empty-icon {
		color: var(--color-text-muted);
		opacity: 0.5;
		margin-bottom: var(--space-sm);
	}

	.empty-state h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.empty-state p {
		margin: 0;
		color: var(--color-text-muted);
		font-size: 0.9375rem;
	}

	.get-apps-btn {
		margin-top: var(--space-md);
		padding: var(--space-sm) var(--space-xl);
		background: var(--color-accent);
		color: white;
		border: none;
		border-radius: var(--radius-md);
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		font-weight: 500;
		text-decoration: none;
		cursor: pointer;
		transition: background 0.15s ease;
	}

	.get-apps-btn:hover {
		background: var(--color-accent-hover);
	}

	/* Error state */
	.error-state {
		display: flex;
		align-items: center;
		justify-content: center;
		color: var(--color-error);
	}

	/* Context Menu */
	.context-menu {
		position: fixed;
		z-index: 1000;
		min-width: 180px;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		box-shadow: 0 4px 20px rgba(0, 0, 0, 0.15);
		padding: var(--space-xs) 0;
		animation: fadeIn 0.1s ease;
	}

	@keyframes fadeIn {
		from { opacity: 0; transform: scale(0.95); }
		to { opacity: 1; transform: scale(1); }
	}

	.context-item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		background: transparent;
		border: none;
		color: var(--color-text);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		text-align: left;
		cursor: pointer;
		transition: background 0.1s ease;
	}

	.context-item:hover {
		background: var(--color-bg-subtle);
	}

	.context-item.danger {
		color: var(--color-error);
	}

	.context-item.danger:hover {
		background: rgba(185, 28, 28, 0.08);
	}

	.context-divider {
		height: 1px;
		margin: var(--space-xs) 0;
		border: none;
		background: var(--color-border);
	}

	/* Widgets Section */
	.widgets {
		margin-top: auto;
		padding-top: var(--space-3xl);
		width: 100%;
		display: flex;
		justify-content: center;
	}

	@media (max-width: 768px) {
		.launcher {
			padding: var(--space-xl);
		}

		.launcher-content {
			padding-top: var(--space-2xl);
		}

		.app-grid {
			gap: var(--space-lg) var(--space-md);
		}

		.widgets {
			padding-top: var(--space-2xl);
		}
	}
</style>
