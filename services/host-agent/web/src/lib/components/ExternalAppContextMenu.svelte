<script lang="ts">
// SPDX-License-Identifier: AGPL-3.0-only
	// Right-click menu for an external app tile: a launcher or a remote install of
	// a catalog app. It is a sibling of AppContextMenu rather than a variant of it
	// because the two act on different entities. An installed app is a workload
	// Bloud owns and renames, gates, and uninstalls; an external record is a
	// pointer the operator declared, so the actions are "edit the pointer" and
	// "stop pointing".
	import { onDestroy, onMount } from 'svelte';
	import { browser } from '$app/environment';
	import Icon from './Icon.svelte';
	import { isAdmin } from '$lib/stores/user';

	interface Props {
		/** External record the menu acts on, or null while nothing is selected. */
		itemId: string | null;
		displayName: string;
		/** Providers get the remote-app copy; a launcher gets the tile copy. */
		isProvider: boolean;
		position: { x: number; y: number };
		onConfigure?: (itemId: string) => void;
		onRemove?: (itemId: string) => void;
		onClose?: () => void;
	}

	let { itemId, displayName, isProvider, position, onConfigure, onRemove, onClose }: Props = $props();

	let menuEl = $state<HTMLDivElement>();

	function handleConfigure() {
		if (!itemId) return;
		onConfigure?.(itemId);
		onClose?.();
	}

	function handleRemove() {
		if (!itemId) return;
		onRemove?.(itemId);
		onClose?.();
	}

	function handleDocumentClick(event: MouseEvent) {
		// Dismiss only on clicks outside the menu. A click on the menu's own
		// padding must not close it before the button's handler runs.
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

{#if itemId && $isAdmin}
	<div
		bind:this={menuEl}
		class="context-menu"
		style="left: {position.x}px; top: {position.y}px;"
		role="menu"
		aria-label={`Menu for ${displayName}`}
		tabindex="-1"
	>
		<button class="context-item" onclick={handleConfigure}>
			<Icon name="settings" size={16} />
			Configure {isProvider ? 'remote app' : 'launcher'}
		</button>
		<hr class="context-divider" />
		<button class="context-item danger" onclick={handleRemove}>
			<Icon name="trash" size={16} />
			Remove
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
