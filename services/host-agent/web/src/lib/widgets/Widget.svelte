<script lang="ts">
	import type { Snippet } from 'svelte';

	interface Props {
		title: string;
		onRemove?: () => void;
		children: Snippet;
	}

	let { title, onRemove, children }: Props = $props();
</script>

<article class="widget">
	<header class="widget-header">
		<h3 class="widget-title">{title}</h3>
		{#if onRemove}
			<button class="remove-btn" onclick={onRemove} aria-label="Remove widget">
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M18 6L6 18M6 6l12 12" />
				</svg>
			</button>
		{/if}
	</header>
	<div class="widget-content">
		{@render children()}
	</div>
</article>

<style>
	.widget {
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		overflow: hidden;
	}

	.widget-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.widget-title {
		margin: 0;
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		letter-spacing: 0.01em;
	}

	.remove-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0;
		transition: opacity 0.15s ease, background 0.15s ease, color 0.15s ease;
	}

	.widget:hover .remove-btn {
		opacity: 1;
	}

	.remove-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.widget-content {
		padding: var(--space-lg);
	}
</style>
