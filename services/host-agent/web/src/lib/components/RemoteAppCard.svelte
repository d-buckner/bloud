<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import AppIcon from './AppIcon.svelte';
	import Icon from './Icon.svelte';
	import type { RemoteApp } from '$lib/types';

	interface Props {
		app: RemoteApp;
		onclick: () => void;
		onremove?: (app: RemoteApp) => void;
	}

	let { app, onclick, onremove }: Props = $props();

	let displayName = $derived(`${app.app_name} (${app.host_label})`);

	function handleRemove(e: MouseEvent | KeyboardEvent) {
		e.stopPropagation();
		onremove?.(app);
	}
</script>

<button class="remote-app-card" {onclick}>
	<div class="card-icon">
		<AppIcon appName={app.app_id} displayName={app.app_name} size="md" />
	</div>
	<div class="card-info">
		<span class="card-name">{displayName}</span>
		<span class="card-badge">shared</span>
	</div>
	{#if onremove}
		<div class="remove-btn" role="button" tabindex="0" onclick={handleRemove} onkeydown={(e) => e.key === 'Enter' && handleRemove(e)} title="Remove shared app">
			<Icon name="trash" size={14} />
		</div>
	{/if}
</button>

<style>
	.remote-app-card {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		padding: var(--space-md) var(--space-lg);
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		cursor: pointer;
		transition: all 0.15s ease;
		width: 100%;
		text-align: left;
	}

	.remote-app-card:hover {
		border-color: var(--color-accent);
		box-shadow: var(--shadow-sm);
	}

	.card-icon {
		flex-shrink: 0;
	}

	.card-info {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		min-width: 0;
	}

	.card-name {
		font-family: var(--font-serif);
		font-size: 0.9375rem;
		color: var(--color-text);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.remove-btn {
		display: none;
		align-items: center;
		justify-content: center;
		margin-left: auto;
		padding: var(--space-xs);
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: var(--color-text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
		flex-shrink: 0;
	}

	.remote-app-card:hover .remove-btn {
		display: flex;
	}

	.remove-btn:hover {
		color: var(--color-error);
		background: rgba(185, 28, 28, 0.08);
		border-color: var(--color-error);
	}

	.card-badge {
		display: inline-block;
		padding: 2px 8px;
		font-size: 0.6875rem;
		font-family: var(--font-serif);
		color: var(--color-text-muted);
		background: var(--color-bg-subtle);
		border: 1px solid var(--color-border);
		border-radius: 9999px;
		white-space: nowrap;
		flex-shrink: 0;
	}
</style>
