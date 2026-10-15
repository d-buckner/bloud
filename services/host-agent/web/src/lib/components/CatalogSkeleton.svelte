<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Skeleton from './Skeleton.svelte';

	interface Props {
		/** How many cards to draw. Enough to fill the grid on a laptop. */
		count?: number;
	}

	// The catalog's own loading state. The catalog is the one route whose content
	// is not in the home snapshot, so it arrives on its own read and needs its own
	// skeleton: "Loading catalog..." over an empty page is the same bare-spinner
	// problem one level down.
	let { count = 8 }: Props = $props();
</script>

<div class="catalog-skeleton" aria-hidden="true">
	<div class="filters">
		<Skeleton width="260px" height="44px" radius="var(--radius-md)" />
		<div class="pills">
			{#each Array(5) as _, i (i)}
				<Skeleton width="64px" height="30px" radius="999px" />
			{/each}
		</div>
	</div>

	<div class="cards">
		{#each Array(count) as _, i (i)}
			<div class="card">
				<Skeleton width="44px" height="44px" radius="10px" />
				<div class="lines">
					<Skeleton width="45%" height="16px" />
					<Skeleton width="90%" height="12px" />
					<Skeleton width="70%" height="12px" />
				</div>
			</div>
		{/each}
	</div>
</div>

<style>
	.catalog-skeleton {
		display: flex;
		flex-direction: column;
		gap: var(--space-lg);
	}

	.filters {
		display: flex;
		flex-direction: column;
		gap: var(--space-md);
		margin-bottom: var(--space-lg);
	}

	.pills {
		display: flex;
		flex-wrap: wrap;
		gap: var(--space-sm);
	}

	/* The same track sizing as `.apps-grid` in the catalog route, so the cards do
	   not reflow when the real ones arrive. */
	.cards {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
		gap: var(--space-lg);
	}

	.card {
		display: flex;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
	}

	.lines {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	@media (max-width: 768px) {
		.cards {
			grid-template-columns: 1fr;
		}
	}
</style>
