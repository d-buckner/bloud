<script lang="ts">
	import AppIcon from './AppIcon.svelte';
	import IconButton from './IconButton.svelte';
	import type { App } from '$lib/types';

	interface Props {
		app: App;
		onremove: () => void;
	}

	let { app, onremove }: Props = $props();
</script>

<div class="app-row">
	<AppIcon appName={app.name} displayName={app.display_name} size="sm" />
	<div class="app-info">
		<span class="app-name">{app.display_name}</span>
		<span class="app-id">{app.name}</span>
	</div>
	<div class="app-status">
		<span class="status-pill" class:running={app.status === 'running'} class:error={app.status === 'stopped' || app.status === 'error' || app.status === 'failed'}>
			{app.status}
		</span>
		{#if app.port}
			<span class="app-port">:{app.port}</span>
		{/if}
	</div>
	<div class="remove-btn">
		<IconButton icon="close" size={16} onclick={onremove} title="Remove" variant="ghost" />
	</div>
</div>

<style>
	.app-row {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		border-bottom: 1px solid var(--color-border-subtle);
		transition: background 0.1s ease;
	}

	.app-row:last-child {
		border-bottom: none;
	}

	.app-row:hover {
		background: var(--color-bg-subtle);
	}

	.app-info {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 2px;
	}

	.app-name {
		font-weight: 500;
	}

	.app-id {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
	}

	.app-status {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.status-pill {
		font-size: 0.75rem;
		padding: 2px 8px;
		border-radius: 9999px;
		text-transform: lowercase;
		background: var(--color-bg-subtle);
		color: var(--color-text-muted);
	}

	.status-pill.running {
		background: var(--color-success-bg);
		color: var(--color-success);
	}

	.status-pill.error {
		background: var(--color-error-bg);
		color: var(--color-error);
	}

	.app-port {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
	}

	.remove-btn {
		opacity: 0;
		transition: opacity 0.15s ease;
	}

	.app-row:hover .remove-btn {
		opacity: 1;
	}

	.remove-btn :global(.icon-btn:hover) {
		background: var(--color-error-bg);
		color: var(--color-error);
	}
</style>
