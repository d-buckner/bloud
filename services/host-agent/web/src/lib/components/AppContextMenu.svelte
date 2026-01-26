<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { browser } from '$app/environment';
	import Icon from './Icon.svelte';
	import type { App } from '$lib/types';

	interface Props {
		app: App | null;
		position: { x: number; y: number };
		onOpenInNewTab?: (app: App) => void;
		onViewLogs?: (app: App) => void;
		onRename?: (app: App) => void;
		onUninstall?: (app: App) => void;
		onClose?: () => void;
	}

	let { app, position, onOpenInNewTab, onViewLogs, onRename, onUninstall, onClose }: Props = $props();

	function handleOpenInNewTab() {
		if (!browser || !app?.port) return;
		window.open(`http://${window.location.hostname}:${app.port}`, '_blank');
		onClose?.();
	}

	function handleViewLogs() {
		if (app) {
			onViewLogs?.(app);
			onClose?.();
		}
	}

	function handleRename() {
		if (app) {
			onRename?.(app);
			onClose?.();
		}
	}

	function handleUninstall() {
		if (app) {
			onUninstall?.(app);
			onClose?.();
		}
	}

	function handleDocumentClick() {
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

{#if app}
	<!-- svelte-ignore a11y_click_events_have_key_events -->
	<div
		class="context-menu"
		style="left: {position.x}px; top: {position.y}px;"
		onclick={(e) => e.stopPropagation()}
		role="menu"
		tabindex="-1"
	>
		<button class="context-item" onclick={handleOpenInNewTab}>
			<Icon name="external-link" size={16} />
			Open in New Tab
		</button>
		<button class="context-item" onclick={handleViewLogs}>
			<Icon name="terminal" size={16} />
			View Logs
		</button>
		<button class="context-item" onclick={handleRename}>
			<Icon name="edit" size={16} />
			Rename
		</button>
		<hr class="context-divider" />
		<button class="context-item danger" onclick={handleUninstall}>
			<Icon name="trash" size={16} />
			Remove App
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
