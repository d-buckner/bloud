<script lang="ts">
	import AppIcon from './AppIcon.svelte';
	import type { RemoteApp } from '$lib/types';

	interface Props {
		app: RemoteApp;
		onclick: () => void;
	}

	let { app, onclick }: Props = $props();

	let displayName = $derived(`${app.app_name} (${app.host_label})`);
</script>

<button class="remote-app-card" {onclick}>
	<div class="card-icon">
		<AppIcon appName={app.app_id} displayName={app.app_name} size="md" />
	</div>
	<div class="card-info">
		<span class="card-name">{displayName}</span>
		<span class="card-badge">shared</span>
	</div>
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
