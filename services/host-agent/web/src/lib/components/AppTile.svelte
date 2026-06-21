<script lang="ts">
	import AppIcon from './AppIcon.svelte';
	import { visibleApps } from '$lib/stores/apps';
	import { type App } from '$lib/types';

	interface Props {
		itemId: string;
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
	}

	let { itemId, onAppClick, onAppContextMenu }: Props = $props();

	let app = $derived($visibleApps.find((a) => a.name === itemId));
	let displayName = $derived(app?.display_name ?? itemId);
	let isInstalling = $derived(!app || app.status === 'installing' || app.status === 'starting');
</script>

<button
	class="app-slot"
	class:installing={isInstalling}
	onclick={() => app && onAppClick?.(app)}
	oncontextmenu={(e) => app && onAppContextMenu?.(e, app)}
	disabled={isInstalling}
>
	<div class="app-icon-wrapper">
		<AppIcon appName={itemId} {displayName} size="lg" transparent={isInstalling} />
		{#if isInstalling}
			<div class="install-spinner"></div>
		{/if}
	</div>
	<span class="app-label">{displayName}</span>
</button>

<style>
	.app-slot {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: var(--space-xs);
		padding: var(--space-sm);
		background: transparent;
		border: none;
		cursor: pointer;
		transition: transform 0.1s ease;
		width: 100%;
		height: 100%;
	}

	.app-slot:hover {
		transform: scale(1.05);
	}

	.app-slot:active {
		transform: scale(0.95);
	}

	.app-slot.installing {
		opacity: 0.7;
		cursor: default;
		pointer-events: none;
	}

	.app-slot.installing:hover {
		transform: none;
	}

	.app-icon-wrapper {
		position: relative;
		width: 52px;
		height: 52px;
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
		to {
			transform: rotate(360deg);
		}
	}

	.app-icon-wrapper :global(.app-icon.size-lg) {
		width: 52px;
		height: 52px;
	}

	.app-icon-wrapper :global(.app-icon.size-lg img) {
		width: 38px;
		height: 38px;
	}

	.app-label {
		font-family: var(--font-sans);
		font-size: 11px;
		font-weight: 500;
		line-height: 1.2;
		color: var(--color-text);
		text-align: center;
		max-width: 80px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
</style>
