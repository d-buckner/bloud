<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import Skeleton from './Skeleton.svelte';
	import { GRID_CELL_HEIGHT, GRID_COLUMNS, GRID_GUTTER } from '$lib/gridGeometry';

	interface Props {
		count?: number;
	}

	// Two rows of the desktop grid.
	let { count = 12 }: Props = $props();

	// Written onto the element rather than typed into the stylesheet below, so the
	// skeleton and the real grid cannot disagree about a number.
	const geometry = [
		`--grid-gutter: ${GRID_GUTTER}px`,
		`--grid-cell-height: ${GRID_CELL_HEIGHT}px`,
		`--grid-columns: ${GRID_COLUMNS}`
	].join('; ');
</script>

<div class="loading-grid-wrap" style={geometry} aria-hidden="true">
	<div class="loading-grid">
		{#each Array(count) as _, i (i)}
			<div class="skeleton-item">
				<Skeleton width="48px" height="48px" radius="12px" />
				<Skeleton width="56px" height="9px" radius="4px" />
			</div>
		{/each}
	</div>
</div>

<style>
	/* GridStack sizes itself from the width of its own element and insets every
	   cell by half the gutter. The skeleton copies both, so a tile does not
	   slide when the real grid replaces it. */
	.loading-grid-wrap {
		container-type: inline-size;
		width: calc(100% + 2 * var(--grid-gutter));
		margin: calc(-1 * var(--grid-gutter)) calc(-1 * var(--grid-gutter)) 0;
	}

	.loading-grid {
		display: grid;
		grid-template-columns: repeat(var(--grid-columns), 1fr);
		gap: var(--grid-gutter);
		padding-inline: calc(var(--grid-gutter) / 2);
	}

	.skeleton-item {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 6px;
		/* One cell, less the gutter: the same arithmetic that gives a real tile
		   its height. */
		height: calc(var(--grid-cell-height) - var(--grid-gutter));
		padding: var(--space-sm);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
	}

	/* The column counts come from `$lib/gridGeometry`, largest width first, and
	   `gridGeometry.test.ts` fails the build if this block stops matching them.
	   A container query rather than a media query because the grid measures its
	   own box, and the sidebar is not part of it. */
	@container (max-width: 820px) {
		.loading-grid {
			grid-template-columns: repeat(4, 1fr);
		}
	}

	@container (max-width: 560px) {
		.loading-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
