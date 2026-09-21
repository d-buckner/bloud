<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	interface Props {
		count?: number;
	}

	// Two rows of the six-column desktop grid.
	let { count = 12 }: Props = $props();
</script>

<div class="loading-grid" aria-hidden="true">
	{#each Array(count) as _, i (i)}
		<div class="skeleton-item">
			<div class="skeleton-icon"></div>
			<div class="skeleton-label"></div>
		</div>
	{/each}
</div>

<style>
	.loading-grid {
		display: grid;
		grid-template-columns: repeat(6, 1fr);
		gap: 12px;
		/* Same gutter trick as the real grid: cancel the outer gutter so the
		   skeleton sits exactly where the tiles will land. */
		width: calc(100% + 24px);
		margin: -12px -12px 0;
	}

	/* Mirrors the grid's column breakpoints so the skeleton does not jump
	   when the real tiles arrive. */
	@media (max-width: 1100px) {
		.loading-grid {
			grid-template-columns: repeat(4, 1fr);
		}
	}

	@media (max-width: 700px) {
		.loading-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}

	.skeleton-item {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 6px;
		height: 104px;
		padding: var(--space-sm);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
	}

	.skeleton-icon {
		width: 44px;
		height: 44px;
		border-radius: 12px;
		background: var(--color-bg-subtle);
		animation: pulse 1.4s ease-in-out infinite;
	}

	.skeleton-label {
		width: 56px;
		height: 9px;
		border-radius: 4px;
		background: var(--color-bg-subtle);
		animation: pulse 1.4s ease-in-out infinite;
		animation-delay: 0.15s;
	}

	@keyframes pulse {
		0%,
		100% {
			opacity: 1;
		}
		50% {
			opacity: 0.45;
		}
	}
</style>
