<script lang="ts">
	import Modal from '$lib/components/Modal.svelte';
	import Button from '$lib/components/Button.svelte';
	import { widgetRegistry } from './registry';
	import { layout, enabledWidgetIds } from '$lib/stores/layout';

	interface Props {
		open: boolean;
		onclose: () => void;
	}

	let { open, onclose }: Props = $props();

	function isEnabled(widgetId: string): boolean {
		return $enabledWidgetIds.includes(widgetId);
	}

	function handleToggle(widgetId: string): void {
		layout.toggleWidget(widgetId);
	}
</script>

<Modal {open} {onclose}>
	<div class="picker">
		<header class="picker-header">
			<h2 class="picker-title">Widgets</h2>
			<button class="close-btn" onclick={onclose} aria-label="Close">
				<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M18 6L6 18M6 6l12 12" />
				</svg>
			</button>
		</header>

		<div class="picker-body">
			<p class="picker-description">Choose which widgets to show on your home screen.</p>

			<div class="widget-list">
				{#each widgetRegistry as widget (widget.id)}
					{@const enabled = isEnabled(widget.id)}
					<button
						class="widget-item"
						class:enabled
						onclick={() => handleToggle(widget.id)}
					>
						<div class="widget-info">
							<span class="widget-name">{widget.name}</span>
							<span class="widget-description">{widget.description}</span>
						</div>
						<div class="widget-toggle" class:active={enabled}>
							<div class="toggle-track">
								<div class="toggle-thumb"></div>
							</div>
						</div>
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
		font-size: 0.9375rem;
	}

	.widget-list {
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
	}

	.widget-item {
		display: flex;
		align-items: center;
		justify-content: space-between;
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
		border-color: var(--color-border);
		background: var(--color-bg-subtle);
	}

	.widget-item.enabled {
		border-color: var(--color-accent);
		background: rgba(28, 25, 23, 0.02);
	}

	.widget-info {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
	}

	.widget-name {
		font-weight: 500;
		font-size: 0.9375rem;
	}

	.widget-description {
		color: var(--color-text-muted);
		font-size: 0.8125rem;
	}

	.widget-toggle {
		flex-shrink: 0;
	}

	.toggle-track {
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
