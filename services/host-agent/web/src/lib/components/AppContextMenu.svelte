<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner
	import { onMount, onDestroy } from 'svelte';
	import { browser } from '$app/environment';
	import Icon from './Icon.svelte';
	import type { App } from '$lib/types';
	import { isAdmin } from '$lib/stores/user';

	interface Props {
		app: App | null;
		position: { x: number; y: number };
		onRename?: (app: App) => void;
		onShare?: (app: App) => void;
		onUninstall?: (app: App) => void;
		onClose?: () => void;
	}

	let { app, position, onRename, onShare, onUninstall, onClose }: Props = $props();

	let menuEl = $state<HTMLDivElement>();

	function handleRename() {
		if (app) {
			onRename?.(app);
			onClose?.();
		}
	}

	function handleShare() {
		if (app) {
			onShare?.(app);
			onClose?.();
		}
	}

	function handleUninstall() {
		if (app) {
			onUninstall?.(app);
			onClose?.();
		}
	}

	function handleDocumentClick(event: MouseEvent) {
		// Dismiss only on clicks outside the menu; clicks inside (buttons, padding)
		// must not close it before the button's own handler runs.
		if (menuEl && event.target instanceof Node && menuEl.contains(event.target)) return;
		onClose?.();
	}

	onMount(() => {
		document.addEventListener('click', handleDocumentClick);
	});

	onDestroy(() => {
		if (browser) {
			document.removeEventListener('click', handleDocumentClick);
		}
	});
</script>

{#if app && $isAdmin}
	<div
		bind:this={menuEl}
		class="context-menu"
		style="left: {position.x}px; top: {position.y}px;"
		role="menu"
		tabindex="-1"
	>
		<button class="context-item" onclick={handleRename}>
			<Icon name="edit" size={16} />
			Rename
		</button>
		<button class="context-item" onclick={handleShare}>
			<Icon name="share" size={16} />
			Share
		</button>
		<hr class="context-divider" />
		<button class="context-item danger" onclick={handleUninstall}>
			<Icon name="trash" size={16} />
			Uninstall
		</button>
	</div>
{/if}

<style>
	.context-menu {
		position: fixed;
		z-index: 1000;
		min-width: 180px;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-lg);
		box-shadow: 0 4px 20px rgba(0, 0, 0, 0.15);
		padding: var(--space-xs) 0;
		animation: fadeIn 0.1s ease;
	}

	@keyframes fadeIn {
		from {
			opacity: 0;
			transform: scale(0.95);
		}
		to {
			opacity: 1;
			transform: scale(1);
		}
	}

	.context-item {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
		width: 100%;
		padding: var(--space-sm) var(--space-md);
		background: transparent;
		border: none;
		color: var(--color-text);
		font-family: var(--font-serif);
		font-size: 0.875rem;
		text-align: left;
		cursor: pointer;
		transition: background 0.1s ease;
	}

	.context-item:hover {
		background: var(--color-bg-subtle);
	}

	.context-item.danger {
		color: var(--color-error);
	}

	.context-item.danger:hover {
		background: rgba(185, 28, 28, 0.08);
	}

	.context-divider {
		height: 1px;
		margin: var(--space-xs) 0;
		border: none;
		background: var(--color-border);
	}
</style>
