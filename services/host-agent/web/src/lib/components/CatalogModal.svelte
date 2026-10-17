<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { onMount } from 'svelte';
	import { SvelteSet } from 'svelte/reactivity';
	import Modal from './Modal.svelte';
	import CloseButton from './CloseButton.svelte';
	import CatalogAppCard from './CatalogAppCard.svelte';
	import CatalogSkeleton from './CatalogSkeleton.svelte';
	import AppDetail from './AppDetail.svelte';
	import Icon from './Icon.svelte';
	import { type CatalogApp, AppStatus } from '$lib/types';
	import { installBrief, type BriefContext } from '$lib/utils/installBrief';
	import { allApps, select, useAppActions } from '$lib/store';
	import { fetchCatalog } from '$lib/clients/catalog';

	/**
	 * The catalog as a modal with two views.
	 *
	 * The list and one app's page share this dialog: choosing a card swaps the
	 * list for the app, and Back swaps it back. Before, the catalog was a route
	 * and the app detail was a second modal stacked on top of it, so the detail
	 * covered the list it came from and the only way back was to close it.
	 *
	 * The route still exists and still renders this (`/catalog`), so the URL is
	 * shareable and the browser Back button closes the modal.
	 */
	interface Props {
		open?: boolean;
		onclose: () => void;
	}

	let { open = true, onclose }: Props = $props();

	const installedApps = select(allApps);

	const appActions = useAppActions();

	let catalogApps = $state<CatalogApp[]>([]);
	let catalogLoading = $state(true);
	let catalogError = $state('');

	let selectedApp = $state<CatalogApp | null>(null);

	let searchQuery = $state('');
	let selectedCategory = $state<string | null>(null);

	function getAppStatus(name: string): AppStatus | undefined {
		return installedApps.current.find((a) => a.catalog_id === name)?.status;
	}

	// The catalog read is the answer to "is this a real app a person can install",
	// and the installed read is the answer to "is it here already". The detail's
	// brief is built from those two facts and nothing else, so it can never claim
	// a wiring the catalog did not declare.
	const briefContext: BriefContext = {
		isInstalled: (name) => getAppStatus(name) !== undefined,
		isUserApp: (name) => catalogApps.some((a) => a.catalogId === name),
		displayName: (name) => catalogApps.find((a) => a.catalogId === name)?.displayName || name
	};

	let detailSections = $derived(selectedApp ? installBrief(selectedApp, briefContext) : []);

	let categories = $derived.by(() => {
		const cats = new SvelteSet<string>();
		for (const app of catalogApps) {
			if (app.category) cats.add(app.category);
		}
		return Array.from(cats).sort();
	});

	let filteredApps = $derived.by(() => {
		let result = catalogApps;

		if (selectedCategory) {
			result = result.filter((app) => app.category === selectedCategory);
		}

		if (searchQuery.trim()) {
			const query = searchQuery.toLowerCase().trim();
			result = result.filter(
				(app) =>
					app.catalogId.toLowerCase().includes(query) ||
					app.displayName?.toLowerCase().includes(query) ||
					app.description?.toLowerCase().includes(query)
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
		// The command never throws: the outcome is the return value, and the store
		// holds the record either way.
		const result = await appActions.install(appName);
		if (!result.ok) console.error('Install failed:', result.error);
	}

	function clearFilters() {
		searchQuery = '';
		selectedCategory = null;
	}
</script>

<Modal {open} {onclose} size="xl" labelledBy={selectedApp ? 'app-detail-title' : 'catalog-title'}>
	{#if selectedApp}
		<AppDetail
			app={selectedApp}
			status={getAppStatus(selectedApp.catalogId)}
			sections={detailSections}
			onback={() => (selectedApp = null)}
			{onclose}
			oninstall={handleInstall}
		/>
	{:else}
		<header class="catalog-header">
			<h2 id="catalog-title">App Catalog</h2>
			<CloseButton onclick={onclose} />
		</header>

		<div class="catalog-body">
			{#if catalogLoading}
				<CatalogSkeleton />
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
							     so a screen reader announced only "edit". aria-controls ties the
							     field to the grid it changes, and the live region below says what
							     the change did, because a filter that redraws a grid in silence has
							     no visible effect for someone who is not looking at it. -->
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
								<button
									class="search-clear"
									onclick={() => (searchQuery = '')}
									aria-label="Clear search"
								>
									<Icon name="close" size={16} />
								</button>
							{/if}
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
									onclick={() => (selectedCategory = null)}
								>
									all
								</button>
								{#each categories as category (category)}
									<button
										class="pill"
										class:active={selectedCategory === category}
										aria-pressed={selectedCategory === category}
										onclick={() => (selectedCategory = category)}
									>
										{category}
									</button>
								{/each}
							</div>
						{/if}
					</div>

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
								onclick={() => (selectedApp = app)}
							/>
						{/each}
					</div>
				{/if}
			{/if}
		</div>
	{/if}
</Modal>

<style>
	.catalog-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border);
	}

	.catalog-header h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 500;
	}

	.catalog-body {
		padding: var(--space-lg);
	}

	.error-state,
	.empty-state {
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

	/* Search and filters share one row and stack on a narrow modal. */
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm) var(--space-md);
		margin-bottom: var(--space-md);
	}

	.filters-row {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		width: 100%;
	}

	.search-wrapper {
		position: relative;
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
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-text) 12%, transparent);
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
		color: var(--color-on-accent);
	}

	.apps-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
		gap: var(--space-md);
	}
</style>
