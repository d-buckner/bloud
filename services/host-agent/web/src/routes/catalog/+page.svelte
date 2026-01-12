<script lang="ts">
	import { onMount } from 'svelte';
	import CatalogAppCard from '$lib/components/CatalogAppCard.svelte';
	import AppDetailModal from '$lib/components/AppDetailModal.svelte';
	import RollbackModal from '$lib/components/RollbackModal.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import type { CatalogApp } from '$lib/types';
	import { apps as installedApps } from '$lib/stores/apps';
	import { installApp } from '$lib/stores/appActions';

	let catalogApps = $state<CatalogApp[]>([]);
	let catalogLoading = $state(true);
	let catalogError = $state('');

	let selectedApp = $state<CatalogApp | null>(null);
	let showRollback = $state(false);

	// Search and filtering
	let searchQuery = $state('');
	let selectedCategory = $state<string | null>(null);

	// Derived: unique categories from catalog apps
	let categories = $derived(() => {
		const cats = new Set<string>();
		for (const app of catalogApps) {
			if (app.category) cats.add(app.category);
		}
		return Array.from(cats).sort();
	});

	// Derived: filtered apps based on search and category
	let filteredApps = $derived(() => {
		let result = catalogApps;

		// Filter by category
		if (selectedCategory) {
			result = result.filter(app => app.category === selectedCategory);
		}

		// Filter by search query
		if (searchQuery.trim()) {
			const query = searchQuery.toLowerCase().trim();
			result = result.filter(app =>
				app.name.toLowerCase().includes(query) ||
				(app.displayName?.toLowerCase().includes(query)) ||
				(app.description?.toLowerCase().includes(query))
			);
		}

		return result;
	});

	// Get the status of an installed app
	function getAppStatus(appName: string): string | null {
		const app = $installedApps.find(a => a.name === appName);
		return app?.status ?? null;
	}

	onMount(async () => {
		try {
			const catalogRes = await fetch('/api/apps');
			const catalogData = await catalogRes.json();
			// Filter out system apps (postgres, traefik, etc) - users shouldn't see these
			catalogApps = (catalogData.apps || []).filter((app: CatalogApp) => !app.isSystem);
		} catch (err) {
			catalogError = err instanceof Error ? err.message : 'Failed to load apps';
		} finally {
			catalogLoading = false;
		}
	});

	async function handleInstall(appName: string, choices: Record<string, string>) {
		try {
			await installApp(appName, choices);
		} catch (err) {
			console.error('Install failed:', err);
		}
	}

	function clearFilters() {
		searchQuery = '';
		selectedCategory = null;
	}
</script>

<svelte:head>
	<title>Catalog · Bloud</title>
</svelte:head>

<div class="page">
	<header class="page-header">
		<div class="header-content">
			<h1>App Catalog</h1>
			<p class="subtitle">One-click installs with automatic integration</p>
		</div>
		<button class="btn btn-secondary" onclick={() => showRollback = true}>
			<Icon name="rollback" size={16} />
			Rollback
		</button>
	</header>

	{#if catalogLoading}
		<div class="loading-state">
			<p>Loading catalog...</p>
		</div>
	{:else if catalogError}
		<div class="error-state">
			<p>{catalogError}</p>
		</div>
	{:else}
		<div class="filters">
			<div class="search-wrapper">
				<span class="search-icon">
					<Icon name="search" size={18} />
				</span>
				<input
					type="text"
					class="search-input"
					placeholder="Search apps..."
					bind:value={searchQuery}
				/>
				{#if searchQuery}
					<button class="search-clear" onclick={() => searchQuery = ''} aria-label="Clear search">
						<Icon name="close" size={16} />
					</button>
				{/if}
			</div>

			{#if categories().length > 0}
				<div class="category-pills">
					<button
						class="pill"
						class:active={selectedCategory === null}
						onclick={() => selectedCategory = null}
					>
						all
					</button>
					{#each categories() as category}
						<button
							class="pill"
							class:active={selectedCategory === category}
							onclick={() => selectedCategory = category}
						>
							{category}
						</button>
					{/each}
				</div>
			{/if}
		</div>

		{#if filteredApps().length === 0}
			<div class="empty-state">
				<p>No apps match your search.</p>
				<button class="clear-filters-btn" onclick={clearFilters}>Clear filters</button>
			</div>
		{:else}
			<div class="apps-grid">
				{#each filteredApps() as app}
					<CatalogAppCard
						{app}
						status={getAppStatus(app.name)}
						onclick={() => selectedApp = app}
					/>
				{/each}
			</div>
		{/if}
	{/if}
</div>

<AppDetailModal
	app={selectedApp}
	status={selectedApp ? getAppStatus(selectedApp.name) : null}
	onclose={() => selectedApp = null}
	oninstall={handleInstall}
/>

<RollbackModal
	open={showRollback}
	onclose={() => showRollback = false}
	onrollback={() => {}}
/>

<style>
	.page {
		padding: var(--space-2xl) var(--space-xl);
	}

	.page-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		margin-bottom: var(--space-2xl);
		padding-bottom: var(--space-xl);
		border-bottom: 1px solid var(--color-border);
	}

	.header-content h1 {
		margin: 0;
		font-size: 1.75rem;
		font-weight: 500;
	}

	.subtitle {
		margin: var(--space-xs) 0 0 0;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.loading-state, .error-state, .empty-state {
		padding: var(--space-2xl);
		text-align: center;
		color: var(--color-text-muted);
	}

	.error-state {
		color: var(--color-error);
	}

	.empty-state p {
		margin: 0 0 var(--space-md) 0;
	}

	.clear-filters-btn {
		font-family: var(--font-serif);
		font-size: 0.875rem;
		color: var(--color-text-secondary);
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		text-decoration: underline;
		text-underline-offset: 2px;
	}

	.clear-filters-btn:hover {
		color: var(--color-text);
	}

	/* Search and Filters */
	.filters {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		margin-bottom: var(--space-xl);
	}

	.search-wrapper {
		position: relative;
		max-width: 320px;
	}

	.search-icon {
		position: absolute;
		left: 12px;
		top: 50%;
		transform: translateY(-50%);
		color: var(--color-text-muted);
		pointer-events: none;
	}

	.search-input {
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		padding-left: 40px;
		padding-right: 36px;
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text);
		transition: border-color 0.15s ease, box-shadow 0.15s ease;
	}

	.search-input::placeholder {
		color: var(--color-text-muted);
	}

	.search-input:focus {
		outline: none;
		border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
	}

	.search-clear {
		position: absolute;
		right: 8px;
		top: 50%;
		transform: translateY(-50%);
		display: flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		padding: 0;
		background: transparent;
		border: none;
		color: var(--color-text-muted);
		cursor: pointer;
		border-radius: var(--radius-sm);
		transition: color 0.1s ease, background 0.1s ease;
	}

	.search-clear:hover {
		color: var(--color-text);
		background: var(--color-bg-subtle);
	}

	.category-pills {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	.pill {
		padding: 6px 14px;
		font-family: var(--font-serif);
		font-size: 0.8125rem;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: 9999px;
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.pill:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.pill.active {
		background: var(--color-accent);
		border-color: var(--color-accent);
		color: white;
	}

	.apps-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
		gap: var(--space-lg);
	}

	.btn {
		display: inline-flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-lg);
		border-radius: var(--radius-md);
		font-size: 0.9375rem;
		font-family: var(--font-serif);
		cursor: pointer;
		border: 1px solid transparent;
		transition: all 0.15s ease;
	}

	.btn-secondary {
		background: var(--color-bg-elevated);
		color: var(--color-text);
		border-color: var(--color-border);
	}

	.btn-secondary:hover:not(:disabled) {
		background: var(--color-bg-subtle);
	}

	@media (max-width: 768px) {
		.apps-grid { grid-template-columns: 1fr; }
		.page-header {
			flex-direction: column;
			gap: var(--space-md);
			align-items: flex-start;
		}
	}
</style>
