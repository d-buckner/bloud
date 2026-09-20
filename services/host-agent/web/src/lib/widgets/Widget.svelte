<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import type { Snippet } from 'svelte';
	import Icon from '$lib/components/Icon.svelte';

	interface Props {
		title: string;
		onRemove?: () => void;
		children: Snippet;
	}

	let { title, onRemove, children }: Props = $props();
</script>

<article class="widget">
	<!-- The header is the drag handle (see GridStackGrid's handleClass) -->
	<header class="widget-header grid-drag-handle">
		<span class="grip" aria-hidden="true"></span>
		<h3 class="widget-title">{title}</h3>
		{#if onRemove}
			<button class="remove-btn" onclick={onRemove} aria-label="Remove {title} widget" title="Remove widget">
				<Icon name="close" size={15} />
			</button>
		{/if}
	</header>
	<div class="widget-content">
		{@render children()}
	</div>
</article>

<style>
	.widget {
		display: flex;
		flex-direction: column;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-sm);
		overflow: hidden;
	}

	.widget-header {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		padding: var(--space-sm) var(--space-sm) var(--space-sm) var(--space-md);
		border-bottom: 1px solid var(--color-border-subtle);
		flex-shrink: 0;
	}

	.widget-header:hover .grip {
		opacity: 0.7;
	}

	/* Dot grid, so the header reads as a handle without another asset */
	.grip {
		width: 12px;
		height: 12px;
		flex-shrink: 0;
		opacity: 0.3;
		color: var(--color-text-muted);
		background-image: radial-gradient(currentColor 1.1px, transparent 1.2px);
		background-size: 5px 5px;
		transition: opacity 0.15s ease;
	}

	.widget-title {
		margin: 0;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--color-text-secondary);
		letter-spacing: 0.01em;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.remove-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 26px;
		height: 26px;
		margin-left: auto;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0;
		transition: opacity 0.15s ease, background 0.15s ease, color 0.15s ease;
	}

	.widget:hover .remove-btn,
	.remove-btn:focus-visible {
		opacity: 1;
	}

	.remove-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.widget-content {
		flex: 1;
		min-height: 0;
		padding: var(--space-md);
		overflow: auto;
	}
</style>
