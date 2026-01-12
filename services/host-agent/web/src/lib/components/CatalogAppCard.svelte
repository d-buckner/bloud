<script lang="ts">
	import AppIcon from './AppIcon.svelte';
	import type { CatalogApp } from '$lib/types';

	interface Props {
		app: CatalogApp;
		status?: string | null;
		onclick: () => void;
	}

	let { app, status = null, onclick }: Props = $props();

	const installed = status === 'running' || status === 'error' || status === 'failed';
	const installing = status === 'installing' || status === 'starting';
	const uninstalling = status === 'uninstalling';

	function formatAppName(name: string): string {
		return name.charAt(0).toUpperCase() + name.slice(1);
	}
</script>

<button
	class="app-card"
	class:installed
	class:installing
	{onclick}
	disabled={installing}
>
	<div class="icon-wrapper">
		<AppIcon appName={app.name} displayName={app.displayName} transparent={installing} />
		{#if installing}
			<div class="install-spinner"></div>
		{/if}
	</div>
	<div class="app-content">
		<div class="app-header">
			<h3 class="app-title">{app.displayName || formatAppName(app.name)}</h3>
			{#if app.category}
				<span class="app-category">{app.category}</span>
			{/if}
		</div>
		{#if app.description}
			<p class="app-description">{app.description}</p>
		{/if}
	</div>
	{#if uninstalling}
		<span class="uninstalling-badge">Uninstalling</span>
	{:else if installed}
		<span class="installed-badge">Installed</span>
	{/if}
</button>

<style>
	.app-card {
		display: flex;
		align-items: flex-start;
		gap: var(--space-md);
		padding: var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		transition: all 0.15s ease;
		cursor: pointer;
		text-align: left;
		width: 100%;
		font-family: inherit;
		font-size: inherit;
	}

	.app-card:hover {
		border-color: var(--color-text-muted);
		transform: translateY(-1px);
		box-shadow: var(--shadow-sm);
	}

	.app-card.installed {
		background: var(--color-success-bg);
		border-color: transparent;
	}

	.app-card:hover :global(.app-icon img) {
		filter: grayscale(0%);
		opacity: 1;
	}

	.app-card.installed :global(.app-icon img) {
		filter: grayscale(0%);
		opacity: 1;
	}

	.app-card :global(.app-icon img) {
		filter: grayscale(100%);
		opacity: 0.8;
		transition: filter 0.2s ease, opacity 0.2s ease;
	}

	.app-content {
		flex: 1;
		min-width: 0;
		overflow: hidden;
	}

	.app-header {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.app-title {
		margin: 0;
		font-size: 1rem;
		font-weight: 500;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.app-category {
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.03em;
		color: var(--color-text-muted);
		background: var(--color-bg-subtle);
		padding: 2px 6px;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
	}

	.app-description {
		margin: var(--space-xs) 0 0 0;
		font-size: 0.8125rem;
		color: var(--color-text-secondary);
		line-height: 1.4;
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.installed-badge {
		font-size: 0.8125rem;
		color: var(--color-success);
		font-style: italic;
	}

	.uninstalling-badge {
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-style: italic;
	}

	.icon-wrapper {
		position: relative;
		flex-shrink: 0;
		width: 44px;
		height: 44px;
	}

	.app-card.installing {
		opacity: 0.7;
		pointer-events: none;
	}

	.install-spinner {
		position: absolute;
		inset: -4px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 1s linear infinite;
	}


	@keyframes spin {
		to { transform: rotate(360deg); }
	}
</style>
