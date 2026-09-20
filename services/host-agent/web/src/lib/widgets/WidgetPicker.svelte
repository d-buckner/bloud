<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import Modal from '$lib/components/Modal.svelte';
	import Button from '$lib/components/Button.svelte';
	import Icon from '$lib/components/Icon.svelte';
	import { widgetRegistry } from './registry';
	import { gridElements, enabledWidgetIds } from '$lib/stores/grid';

	interface Props {
		open: boolean;
		onclose: () => void;
	}

	let { open, onclose }: Props = $props();

	function isEnabled(widgetId: string): boolean {
		return $enabledWidgetIds.includes(widgetId);
	}
</script>

<Modal {open} {onclose}>
	<div class="picker">
		<header class="picker-header">
			<h2 class="picker-title">Widgets</h2>
			<button class="close-btn" onclick={onclose} aria-label="Close">
				<Icon name="close" size={18} />
			</button>
		</header>

		<div class="picker-body">
			<p class="picker-description">
				Widgets live on your home screen next to your apps. Drag a widget by its header to move
				it, or by its corner to resize.
			</p>

			<div class="widget-list">
				{#each widgetRegistry as widget (widget.id)}
					{@const enabled = isEnabled(widget.id)}
					<button class="widget-item" class:enabled onclick={() => gridElements.toggleWidget(widget.id)}>
						<span class="widget-icon">
							<Icon name={widget.icon} size={18} />
						</span>
						<span class="widget-info">
							<span class="widget-name">
								{widget.name}
								<span class="widget-size">{widget.size.cols} × {widget.size.rows}</span>
							</span>
							<span class="widget-description">{widget.description}</span>
						</span>
						<span class="widget-toggle" class:active={enabled}>
							<span class="toggle-track">
								<span class="toggle-thumb"></span>
							</span>
						</span>
					</button>
				{/each}
			</div>
		</div>

		<footer class="picker-footer">
			<Button variant="primary" onclick={onclose}>Done</Button>
		</footer>
	</div>
</Modal>

<style>
	.picker {
		display: flex;
		flex-direction: column;
	}

	.picker-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
	}

	.picker-title {
		margin: 0;
		font-size: 1.125rem;
		font-weight: 500;
	}

	.close-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 32px;
		height: 32px;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: var(--radius-md);
		color: var(--color-text-secondary);
		cursor: pointer;
		transition: background 0.15s ease, color 0.15s ease;
	}

	.close-btn:hover {
		background: var(--color-bg-subtle);
		color: var(--color-text);
	}

	.picker-body {
		padding: var(--space-lg);
	}

	.picker-description {
		margin: 0 0 var(--space-lg);
		color: var(--color-text-secondary);
		font-size: 0.875rem;
	}

	.widget-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.widget-item {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md);
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: border-color 0.15s ease, background 0.15s ease;
		text-align: left;
		width: 100%;
	}

	.widget-item:hover {
		background: var(--color-bg-subtle);
	}

	.widget-item.enabled {
		border-color: var(--color-border-strong);
		background: var(--color-bg-elevated);
	}

	.widget-icon {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 36px;
		height: 36px;
		flex-shrink: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		background: var(--color-bg-elevated);
		color: var(--color-text-secondary);
	}

	.widget-info {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.widget-name {
		display: flex;
		align-items: baseline;
		gap: var(--space-sm);
		font-weight: 500;
		font-size: 0.9375rem;
	}

	.widget-size {
		font-family: var(--font-sans);
		font-size: 0.6875rem;
		color: var(--color-text-muted);
		font-variant-numeric: tabular-nums;
	}

	.widget-description {
		color: var(--color-text-muted);
		font-size: 0.8125rem;
	}

	.widget-toggle {
		margin-left: auto;
		flex-shrink: 0;
	}

	.toggle-track {
		display: block;
		width: 44px;
		height: 24px;
		background: var(--color-border);
		border-radius: 12px;
		padding: 2px;
		transition: background 0.2s ease;
	}

	.widget-toggle.active .toggle-track {
		background: var(--color-accent);
	}

	.toggle-thumb {
		display: block;
		width: 20px;
		height: 20px;
		background: white;
		border-radius: 50%;
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.15);
		transition: transform 0.2s ease;
	}

	.widget-toggle.active .toggle-thumb {
		transform: translateX(20px);
	}

	.picker-footer {
		display: flex;
		justify-content: flex-end;
		padding: var(--space-lg);
		border-top: 1px solid var(--color-border-subtle);
	}
</style>
