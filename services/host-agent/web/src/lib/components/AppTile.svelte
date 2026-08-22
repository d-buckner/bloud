<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import AppIcon from './AppIcon.svelte';
	import { visibleApps, loading } from '$lib/stores/apps';
	import { appProgress } from '$lib/stores/appProgress';
	import { type App } from '$lib/types';

	interface Props {
		itemId: string;
		onAppClick?: (app: App) => void;
		onAppContextMenu?: (e: MouseEvent, app: App) => void;
	}

	let { itemId, onAppClick, onAppContextMenu }: Props = $props();

	let app = $derived($visibleApps.find((a) => a.catalog_id === itemId));
	let progress = $derived(app ? $appProgress[app.catalog_id] ?? null : null);
	let displayName = $derived(app?.display_name ?? itemId);
	let status = $derived(app?.status ?? null);
	let isInstalling = $derived(
		app ? status === 'installing' || status === 'starting' : !$loading
	);
	let isFailed = $derived(status === 'failed');
	let isDegraded = $derived(status === 'error');
	let isStopped = $derived(status === 'stopped');

	// Phase label under the icon while work is in flight. The spinner is the
	// fallback for the brief window before the first node/pull event arrives.
	let phase = $derived(progress?.phase ?? null);
	let phaseLabel = $derived.by(() => {
		if (!isInstalling || !phase || phase === 'failed') return null;
		const base: Record<string, string> = {
			queued: 'Queued',
			pulling: 'Pulling image',
			configuring: 'Configuring',
			starting: 'Starting',
			finalizing: 'Finalizing',
			running: 'Ready'
		};
		const label = base[phase] ?? phase;
		if (phase === 'pulling' && progress?.percent != null) return `${label} · ${progress.percent}%`;
		return label;
	});
	let showSpinner = $derived(isInstalling && !phaseLabel);
	let showProgressBar = $derived(
		isInstalling && phase === 'pulling' && progress?.percent != null
	);
</script>

<!-- Tiles stay clickable in every state: installing/failed open the install
     detail modal (investigation is the point), running opens the app. -->
<button
	class="app-slot"
	class:installing={isInstalling}
	class:failed={isFailed}
	class:degraded={isDegraded}
	class:stopped={isStopped}
	onclick={() => app && onAppClick?.(app)}
	oncontextmenu={(e) => app && onAppContextMenu?.(e, app)}
>
	<div class="app-icon-wrapper">
		<AppIcon appName={itemId} displayName={displayName} size="lg" transparent={isInstalling} />
		{#if showSpinner}
			<div class="install-spinner"></div>
		{/if}
	</div>
	<span class="app-label">{displayName}</span>
	{#if phaseLabel}
		<span class="phase-label">{phaseLabel}</span>
	{:else if isFailed}
		<span class="phase-label failed">Failed</span>
	{:else if isDegraded}
		<span class="phase-label degraded">Degraded</span>
	{/if}
	{#if showProgressBar}
		<div class="progress-track">
			<div class="progress-fill" style="width: {progress?.percent ?? 0}%"></div>
		</div>
	{/if}
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
		opacity: 0.85;
	}

	.app-slot.stopped {
		opacity: 0.5;
	}

	.app-icon-wrapper {
		position: relative;
		width: 52px;
		height: 52px;
		border-radius: 50%;
	}

	.app-slot.failed .app-icon-wrapper {
		box-shadow: 0 0 0 2px var(--color-error);
	}

	.app-slot.degraded .app-icon-wrapper {
		box-shadow: 0 0 0 2px var(--color-warning, #d97706);
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

	.phase-label {
		font-size: 9px;
		font-weight: 500;
		line-height: 1.2;
		color: var(--color-text-muted);
		text-align: center;
		max-width: 80px;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.phase-label.failed {
		color: var(--color-error);
	}

	.phase-label.degraded {
		color: var(--color-warning, #d97706);
	}

	.progress-track {
		position: absolute;
		bottom: 0;
		left: 10%;
		width: 80%;
		height: 3px;
		border-radius: 2px;
		background: var(--color-border-subtle, var(--color-border));
		overflow: hidden;
	}

	.progress-fill {
		height: 100%;
		border-radius: 2px;
		background: var(--color-accent);
		transition: width 0.4s ease;
	}
</style>
