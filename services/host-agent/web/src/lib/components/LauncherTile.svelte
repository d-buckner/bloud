<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	import { launchers } from '$lib/stores/launchers';

	interface Props {
		itemId: string;
	}

	let { itemId }: Props = $props();

	let launcher = $derived($launchers.find((l) => l.id === itemId));
	let displayName = $derived(launcher?.name ?? itemId);
	let url = $derived(launcher?.url ?? '');

	function activate(event: MouseEvent | KeyboardEvent) {
		if (event instanceof KeyboardEvent && event.key !== 'Enter' && event.key !== ' ') return;
		if (!url) return;
		event.preventDefault();
		window.open(url, '_blank', 'noopener,noreferrer');
	}
</script>

<!-- A launcher is a tile with no lifecycle: it always opens the operator's URL
     in a new tab. The element is a div (not a button) so GridStack's drag
     handler does not skip it, matching AppTile. -->
<div
	class="app-slot app-tile launcher-tile grid-drag-handle"
	role="button"
	tabindex="0"
	aria-label={displayName}
	title={displayName}
	onclick={activate}
	onkeydown={activate}
>
	<div class="app-icon-wrapper">
		{#if launcher?.icon}
			<img src={launcher.icon} alt="" class="launcher-icon" />
		{:else}
			<span class="launcher-glyph" aria-hidden="true">↗</span>
		{/if}
	</div>
	<span class="app-label">{displayName}</span>
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

	.app-icon-wrapper {
		width: 48px;
		height: 48px;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.launcher-icon {
		width: 38px;
		height: 38px;
		border-radius: 12px;
		object-fit: cover;
	}

	.launcher-glyph {
		font-size: 26px;
		line-height: 1;
		color: var(--color-text-muted);
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
</style>
