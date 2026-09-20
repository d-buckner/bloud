<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import AppIcon from './AppIcon.svelte';
	import { visibleApps, loading } from '$lib/stores/apps';
	import { type App } from '$lib/types';

	interface Props {
		itemId: string;
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
	}

	let { itemId, onAppClick, onAppContextMenu }: Props = $props();

	let app = $derived($visibleApps.find((a) => a.catalog_id === itemId));
	let displayName = $derived(app?.display_name ?? itemId);
	let status = $derived(app?.status ?? null);
	let isInstalling = $derived(app ? status === 'installing' || status === 'starting' : !$loading);
	let isFailed = $derived(status === 'failed');
	let isDegraded = $derived(status === 'error');
	// While installing, the tile shows only the app name; the icon itself
	// carries the loading animation (a spinning accent ring). Live phase
	// detail lives in the install detail modal, not on the tile.
	let isStopped = $derived(status === 'stopped');

	let stateLabel = $derived(isFailed ? 'Failed' : isDegraded ? 'Degraded' : '');
	let ariaState = $derived(
		[isInstalling ? 'installing' : '', stateLabel.toLowerCase()].filter(Boolean).join(' ')
	);

	function activate(event: MouseEvent | KeyboardEvent) {
		if (event instanceof KeyboardEvent && event.key !== 'Enter' && event.key !== ' ') return;
		if (!app) return;
		event.preventDefault();
		onAppClick?.(app);
	}
</script>

<!-- Tiles stay activatable in every state: installing/failed open the install
     detail modal (investigation is the point), running opens the app. The
     element is a div rather than a button so GridStack's drag handler does not
     skip it (it ignores mousedowns that land on buttons). -->
<div
	class="app-slot app-tile grid-drag-handle"
	class:installing={isInstalling}
	class:failed={isFailed}
	class:degraded={isDegraded}
	class:stopped={isStopped}
	role="button"
	tabindex="0"
	aria-label={ariaState ? `${displayName}: ${ariaState}` : displayName}
	title={displayName}
	onclick={activate}
	onkeydown={activate}
	oncontextmenu={(e) => app && onAppContextMenu?.(e, app)}
>
	<div class="app-icon-wrapper">
		<AppIcon appName={itemId} displayName={displayName} size="lg" transparent={isInstalling} />
		{#if isInstalling}
			<div class="install-spinner" role="status" aria-label="Installing"></div>
		{/if}
	</div>
	<span class="app-label">{displayName}</span>
	{#if stateLabel}
		<span class="phase-label" class:failed={isFailed} class:degraded={isDegraded}>{stateLabel}</span>
	{/if}
</div>

<style>
	.app-tile {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 6px;
		padding: var(--space-sm);
		width: 100%;
		height: 100%;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		color: var(--color-text);
		text-align: center;
		transition:
			border-color 0.15s ease,
			box-shadow 0.15s ease,
			transform 0.15s ease;
	}

	.app-tile:hover {
		border-color: var(--color-border-strong);
		box-shadow: var(--shadow-sm);
		transform: translateY(-1px);
	}

	.app-tile:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.app-tile.stopped {
		opacity: 0.55;
	}

	.app-icon-wrapper {
		position: relative;
		width: 48px;
		height: 48px;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.app-icon-wrapper :global(.app-icon.size-lg) {
		width: 48px;
		height: 48px;
		border-radius: 12px;
	}

	.app-icon-wrapper :global(.app-icon.size-lg img) {
		width: 38px;
		height: 38px;
	}

	.app-tile.failed .app-icon-wrapper {
		box-shadow: 0 0 0 2px var(--color-error);
		border-radius: 12px;
	}

	.app-tile.degraded .app-icon-wrapper {
		box-shadow: 0 0 0 2px var(--color-warning);
		border-radius: 12px;
	}

	.install-spinner {
		position: absolute;
		inset: -4px;
		border: 2px solid var(--color-border);
		border-top-color: var(--color-accent);
		border-radius: 50%;
		animation: spin 0.9s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}

	.app-label {
		font-family: var(--font-sans);
		font-size: 12px;
		font-weight: 500;
		line-height: 1.25;
		color: var(--color-text);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
		overflow-wrap: anywhere;
		width: 100%;
	}

	.phase-label {
		font-family: var(--font-sans);
		font-size: 10px;
		font-weight: 500;
		line-height: 1.2;
		color: var(--color-text-muted);
	}

	.phase-label.failed {
		color: var(--color-error);
	}

	.phase-label.degraded {
		color: var(--color-warning);
	}
</style>
