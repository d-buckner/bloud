<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import Icon from './Icon.svelte';

	interface Props {
		onopen: () => void;
		onuninstall: () => void;
	}

	let { onopen, onuninstall }: Props = $props();

	let isOpen = $state(false);
	let menuRef = $state<HTMLDivElement | null>(null);
	let buttonRef = $state<HTMLButtonElement | null>(null);

	function toggle(e: MouseEvent) {
		e.stopPropagation();
		isOpen = !isOpen;
	}

	function handleOpen(e: MouseEvent) {
		e.stopPropagation();
		isOpen = false;
		onopen();
	}

	function handleUninstall(e: MouseEvent) {
		e.stopPropagation();
		isOpen = false;
		onuninstall();
	}

	function handleClickOutside(e: MouseEvent) {
		if (isOpen && menuRef && !menuRef.contains(e.target as Node) && !buttonRef?.contains(e.target as Node)) {
			isOpen = false;
		}
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && isOpen) {
			isOpen = false;
		}
	}

	onMount(() => {
		document.addEventListener('click', handleClickOutside);
		document.addEventListener('keydown', handleKeydown);
	});

	onDestroy(() => {
		document.removeEventListener('click', handleClickOutside);
		document.removeEventListener('keydown', handleKeydown);
	});
</script>

<div class="app-menu-container">
	<button
		bind:this={buttonRef}
		class="menu-trigger"
		onclick={toggle}
		title="App options"
		aria-label="App options"
		aria-expanded={isOpen}
	>
		<Icon name="more-vertical" size={16} />
	</button>

	{#if isOpen}
		<div bind:this={menuRef} class="menu-dropdown">
			<button class="menu-item" onclick={handleOpen}>
				<Icon name="external-link" size={14} />
				Open in new tab
			</button>
			<hr class="menu-separator" />
			<button class="menu-item menu-item-danger" onclick={handleUninstall}>
				<Icon name="trash" size={14} />
				Uninstall
			</button>
		</div>
	{/if}
</div>

<style>
	.app-menu-container {
		position: relative;
		z-index: 10;
		margin-left: auto;
	}

	.menu-trigger {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 28px;
		height: 28px;
		padding: 0;
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		color: var(--color-text-muted);
		cursor: pointer;
		opacity: 0;
		transition: all 0.15s ease;
	}

	.menu-trigger:hover {
		background: var(--color-bg-subtle);
		border-color: var(--color-border);
		color: var(--color-text);
	}

	.menu-trigger:focus {
		opacity: 1;
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	/* Show on parent hover - controlled by parent */
	:global(.app-card:hover) .menu-trigger,
	:global(.app-card:focus-within) .menu-trigger {
		opacity: 1;
	}

	.menu-dropdown {
		position: absolute;
		top: 100%;
		right: 0;
		margin-top: 4px;
		min-width: 180px;
		background: var(--color-bg-elevated);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		box-shadow: var(--shadow-md);
		overflow: hidden;
		padding: var(--space-xs) 0;
	}

	.menu-item {
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
		white-space: nowrap;
	}

	.menu-item:hover {
		background: var(--color-bg-subtle);
	}

	.menu-item-danger {
		color: var(--color-error);
	}

	.menu-item-danger:hover {
		background: rgba(185, 28, 28, 0.08);
	}

	.menu-separator {
		height: 1px;
		margin: var(--space-xs) 0;
		border: none;
		background: var(--color-border);
	}
</style>
