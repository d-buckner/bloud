<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import CatalogAppCard from '$lib/components/CatalogAppCard.svelte';
	import AppDetailModal from '$lib/components/AppDetailModal.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { type CatalogApp, AppStatus } from '$lib/types';
	import { apps as installedApps } from '$lib/stores/apps';
	import { installApp } from '$lib/clients/appFacade';
	import { fetchCatalog } from '$lib/clients/catalog';

	let catalogApps = $state<CatalogApp[]>([]);
	let catalogLoading = $state(true);
	let catalogError = $state('');

	let selectedApp = $state<CatalogApp | null>(null);

	function getAppStatus(name: string): AppStatus | undefined {
		return $installedApps.find((a) => a.catalog_id === name)?.status;
	}

	// Search and filtering
	let searchQuery = $state('');
	let selectedCategory = $state<string | null>(null);

	// Derived: unique categories from catalog apps
	let categories = $derived.by(() => {
		const cats = new SvelteSet<string>();
		for (const app of catalogApps) {
			if (app.category) cats.add(app.category);
		}
		return Array.from(cats).sort();
	});

	// Derived: filtered apps based on search and category
	let filteredApps = $derived.by(() => {
		let result = catalogApps;

		// Filter by category
		if (selectedCategory) {
			result = result.filter(app => app.category === selectedCategory);
		}

		// Filter by search query
		if (searchQuery.trim()) {
			const query = searchQuery.toLowerCase().trim();
			result = result.filter(app =>
				app.catalogId.toLowerCase().includes(query) ||
				(app.displayName?.toLowerCase().includes(query)) ||
				(app.description?.toLowerCase().includes(query))
			);
		}

		return result;
	});

	// Announced, not printed next to the grid: the count is the only feedback a
	// filter gives to someone who cannot see the grid redraw.
	let resultCount = $derived(filteredApps.length);


	onMount(async () => {
		try {
			catalogApps = await fetchCatalog();
		} catch (err) {
			catalogError = err instanceof Error ? err.message : 'Failed to load apps';
		} finally {
			catalogLoading = false;
		}
	});

	async function handleInstall(appName: string) {
		try {
			await installApp(appName);
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
			<div class="filters-row">
				<div class="search-wrapper">
					<span class="search-icon" aria-hidden="true">
						<Icon name="search" size={18} />
					</span>
					<!-- type="search" so the browser offers its own clear affordance and a
					     phone shows the right keyboard; the field had no name at all before,
					     so a screen reader announced only "edit". aria-controls is what ties
					     the field to the grid it changes, and the live region below is what
					     says what the change did, because a filter that redraws a grid in
					     silence has no visible effect for someone who is not looking at it. -->
					<input
						type="search"
						class="search-input"
						placeholder="Search apps..."
						aria-label="Search apps"
						autocomplete="off"
						aria-controls="catalog-results"
						bind:value={searchQuery}
					/>
					{#if searchQuery}
						<button class="search-clear" onclick={() => searchQuery = ''} aria-label="Clear search">
							<Icon name="close" size={16} />
						</button>
					{/if}
				</div>
			</div>

			{#if categories.length > 0}
				<!-- aria-pressed rather than a tablist: these are filters over one list,
				     not views of separate content, and the active state was a black pill
				     with nothing behind it. -->
				<div class="category-pills" role="group" aria-label="Filter apps by category">
					<button
						class="pill"
						class:active={selectedCategory === null}
						aria-pressed={selectedCategory === null}
						onclick={() => selectedCategory = null}
					>
						all
					</button>
					{#each categories as category (category)}
						<button
							class="pill"
							class:active={selectedCategory === category}
							aria-pressed={selectedCategory === category}
							onclick={() => selectedCategory = category}
						>
							{category}
						</button>
					{/each}
				</div>
			{/if}

			<p class="visually-hidden" role="status" aria-live="polite">
				{resultCount}
				{resultCount === 1 ? 'app' : 'apps'} match
				{#if selectedCategory}in {selectedCategory}{/if}
			</p>
		</div>

		{#if filteredApps.length === 0}
			<div class="empty-state">
				<p>No apps match your search.</p>
				<button class="clear-filters-btn" onclick={clearFilters}>Clear filters</button>
			</div>
		{:else}
			<div class="apps-grid" id="catalog-results">
				{#each filteredApps as app (app.catalogId)}
					<CatalogAppCard
						{app}
						status={getAppStatus(app.catalogId)}
						onclick={() => selectedApp = app}
					/>
				{/each}
			</div>
		{/if}
	{/if}
</div>

<AppDetailModal
	app={selectedApp}
	status={selectedApp ? getAppStatus(selectedApp.catalogId) : null}
	onclose={() => selectedApp = null}
	oninstall={handleInstall}
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

	.filters-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
	}

	.search-wrapper {
		position: relative;
		/* Width available, capped, rather than a fixed box: a 320px field in a
		   390px viewport with page padding leaves nothing, and the audit measured
			 the catalog 140px wider than the screen it was on. */
		width: 100%;
		max-width: 320px;
		min-width: 0;
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
		/* Room for a 44px clear button, which is what the tap-target rule makes it. */
		padding-right: 48px;
		min-width: 0;
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
				border-color: var(--color-accent);
		box-shadow: 0 0 0 3px rgba(28, 25, 23, 0.08);
	}

	.search-clear {
		position: absolute;
		right: 2px;
		top: 50%;
		transform: translateY(-50%);
		display: flex;
		align-items: center;
		justify-content: center;
		/* The global 44px minimum applies to this button, so the field has to be
		   tall enough to hold it and the glyph has to stay centred inside it. */
		width: var(--tap-target-min);
		height: var(--tap-target-min);
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

	@media (max-width: 768px) {
		.apps-grid { grid-template-columns: 1fr; }
		.page-header {
			flex-direction: column;
			gap: var(--space-md);
			align-items: flex-start;
		}
	}

	/* The page gutter is 32px a side, which is 64px of a 390px screen before any
	   content. Trimming it is what gives the chip row and the search field room
	   to stop pushing the document wider than the viewport. */
	@media (max-width: 480px) {
		.page {
			padding: var(--space-lg) var(--space-md);
		}

		.search-wrapper {
			max-width: none;
		}
	}
</style>
