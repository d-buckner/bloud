<script lang="ts">
	interface Props {
		count?: number;
	}

	let { count = 8 }: Props = $props();
</script>

<div class="loading-grid">
	{#each Array(count) as _}
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
		gap: var(--space-lg);
		max-width: 1200px;
		margin: 0 auto;
		width: 100%;
	}

	.skeleton-item {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-md);
	}

	.skeleton-icon {
		width: 60px;
		height: 60px;
		border-radius: 14px;
		background: linear-gradient(
			90deg,
			var(--color-bg-subtle) 25%,
			var(--color-bg-elevated) 50%,
			var(--color-bg-subtle) 75%
		);
		background-size: 200% 100%;
		animation: shimmer 1.5s infinite;
	}

	.skeleton-label {
		width: 50px;
		height: 11px;
		border-radius: 4px;
		background: linear-gradient(
			90deg,
			var(--color-bg-subtle) 25%,
			var(--color-bg-elevated) 50%,
			var(--color-bg-subtle) 75%
		);
		background-size: 200% 100%;
		animation: shimmer 1.5s infinite;
		animation-delay: 0.1s;
	}

	@keyframes shimmer {
		0% {
			background-position: 200% 0;
		}
		100% {
			background-position: -200% 0;
		}
	}

	@media (max-width: 768px) {
		.loading-grid {
			grid-template-columns: repeat(3, 1fr);
		}
	}

	@media (max-width: 400px) {
		.loading-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
